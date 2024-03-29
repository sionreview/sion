package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"

	// "strings"

	"time"

	"github.com/aws/aws-sdk-go/aws"
	awsRequest "github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/cespare/xxhash"

	mys3 "github.com/sionreview/sion/common/aws/s3"
	protocol "github.com/sionreview/sion/common/types"
	"github.com/sionreview/sion/common/util"
	"github.com/sionreview/sion/lambda/types"
)

const (
	CHUNK_KEY = "%schunks/%s%s"

	// StorageSignalFlagForceCommit Indicate a PersistentStorage to commit regardless of any queued operations.
	StorageSignalFlagForceCommit = 0x0001
)

var (
	Concurrency = types.DownloadConcurrency
	Buckets     = 1
	Hasher      = &util.Hasher{}
	Segmentor   = regexp.MustCompile(`[0-9a-f]{2}`)

	ErrTrackerNotStarted = errors.New("tracker not started")
)

type PersistentStorageSignal uint32

func (sig PersistentStorageSignal) Flags() uint32 {
	return 0
}

type PersistHelper interface {
	// onPersisted can be implemented to do something after a chunk is persisted.
	onPersisted(*types.OpWrapper)

	// signal returns true if tracker has started
	signal(StorageSignal) bool

	// getSignalTracker returns a readonly channel that tracks the signal for this storage.
	getSignalTracker() <-chan StorageSignal

	// onSignalTracker can be implemented to execution signal action.
	// Returns true to stop the tracker
	onSignalTracker(StorageSignal) bool
}

// PersistentStorage Storage with S3 as persistent layer
type PersistentStorage struct {
	*Storage

	chanOps        chan *types.OpWrapper // NOTE: implement an unbounded channel if neccessary.
	signalTracker  chan StorageSignal
	trackerStopped chan StorageSignal
	persistHelper  PersistHelper

	// Persistency backpack
	s3bucket        string
	s3bucketDefault string
	s3prefix        string
	s3Downloader    *mys3.Downloader
}

func NewPersistentStorage(id uint64, cap uint64) *PersistentStorage {
	storage := &PersistentStorage{
		Storage: NewStorage(id, cap),
	}
	storage.helper = storage
	storage.persistHelper = storage
	return storage
}

// Storage Implementation
func (s *PersistentStorage) getWithOption(key string, opt *types.OpWrapper) (*types.Chunk, *types.OpRet) {
	chunk, ok := s.helper.get(key)
	if !ok {
		// No entry
		return nil, types.OpError(types.ErrNotFound)
	}

	// For objects in buffer, we will not touch object on accessing.
	if opt == nil || !opt.Accessed {
		chunk.Access()
	}
	if chunk.IsDeleted() {
		return nil, types.OpErrorWithMessage(types.ErrDeleted, chunk.Note)
	} else if chunk.IsAvailable() {
		// Ensure val is available regardless chunk is deleted or not.
		return chunk, types.OpSuccess()
	}

	// Recovering, wait to be notified.
	chunk.WaitRecovered()

	// Check again
	if opt == nil || !opt.Accessed {
		chunk.Access()
	}
	if chunk.IsDeleted() {
		return nil, types.OpErrorWithMessage(types.ErrDeleted, chunk.Note)
	} else if chunk.IsAvailable() {
		// Ensure val is available regardless chunk is deleted or not.
		return chunk, types.OpSuccess()
	} else {
		return nil, types.OpError(types.ErrIncomplete)
	}
}

func (s *PersistentStorage) setWithOption(key string, chunk *types.Chunk, opt *types.OpWrapper) *types.OpRet {
	s.Storage.setWithOption(key, chunk, opt)
	if s.chanOps != nil {
		op := &types.OpWrapper{
			LineageOp: types.LineageOp{
				Op:       types.OP_SET,
				Key:      key,
				Id:       chunk.Id,
				Size:     chunk.Size,
				Accessed: chunk.Accessed,
			},
			OpRet: types.OpDelayedSuccess(),
			Chunk: chunk,
			Body:  chunk.Body,
		}
		if chunk.BuffIdx > 0 {
			op.LineageOp.BIdx = chunk.BuffIdx
		}
		// Copy options. Field "Persisted" only so far.
		if opt != nil {
			op.Persisted = opt.Persisted
		}
		s.chanOps <- op
		// s.log.Debug("local set ok, key %v", key)
		return op.OpRet
	} else {
		return types.OpSuccess()
	}
}

func (s *PersistentStorage) newChunk(key string, chunkId string, size uint64, val []byte) *types.Chunk {
	chunk := types.NewChunk(key, chunkId, val)
	chunk.Size = size
	chunk.Term = 1
	chunk.Bucket = s.getBucket(key)
	return chunk
}

func (s *PersistentStorage) SetRecovery(key string, chunkId string, size uint64, opts int) *types.OpRet {
	_, err := s.helper.getWithOption(key, nil)
	if err.Error() == nil {
		return err
	}

	emptyChunk := s.helper.newChunk(key, chunkId, size, nil)
	emptyChunk.Delete("prepare recovery") // Delete to ensure call PrepareRecover() succssfully
	emptyChunk.PrepareRecover()
	inserted, loaded := s.repo.GetOrInsert(key, emptyChunk)
	chunk := inserted.(*types.Chunk)
	// Legacy chunk that failed to download
	if loaded && chunk.IsIncomplete() {
		// Replace chunk
		changed := s.repo.Cas(key, chunk, emptyChunk)
		if changed {
			chunk = emptyChunk
			loaded = false
		} else {
			inserted, _ = s.repo.Get(key)
			chunk = inserted.(*types.Chunk)
			loaded = true
		}
	}
	if loaded && !chunk.PrepareRecover() {
		chunk.WaitRecovered()
		if chunk.IsAvailable() {
			return types.OpSuccess()
		} else {
			return types.OpError(types.ErrIncomplete)
		}
	}

	if opts&protocol.REQUEST_GET_OPTION_BUFFER > 0 {
		chunk.BuffIdx = types.CHUNK_TOBEBUFFERED
	}
	opt, ok := s.helper.validate(chunk, nil)
	if !ok {
		return types.OpError(ErrOOStorage)
	}

	chunk.Body = make([]byte, size) // Pre-allocate fixed sized buffer.
	chunk.StartRecover()
	downloader := s.getS3Downloader()
	ctx := aws.BackgroundContext()
	ctx = context.WithValue(ctx, &ContextKeyLog, s.log)
	if err := downloader.Download(ctx, func(input *mys3.BatchDownloadObject) {
		input.Object.Bucket = s.bucket(&chunk.Bucket)
		input.Object.Key = aws.String(s.getS3Key(key))
		input.Size = size
		input.Writer = aws.NewWriteAtBuffer(chunk.Body)
		input.After = func() error {
			chunk.AddRecovered(uint64(input.Downloaded))
			return nil
		}
	}); err != nil {
		chunk.NotifyRecovered()
		return types.OpError(err)
	}

	// This is to reuse persistent implementation.
	// Chunk inserted previously will be loaded, and no new chunk will be created.
	if opt == nil {
		opt = &types.OpWrapper{}
	}
	opt.Persisted = true
	ret := s.helper.setWithOption(key, chunk, opt)
	chunk.NotifyRecovered()
	return ret
}

func (s *PersistentStorage) delWithOption(chunk *types.Chunk, reason string, opt *types.OpWrapper) *types.OpRet {
	s.Storage.delWithOption(chunk, reason, opt)

	if s.chanOps != nil {
		op := &types.OpWrapper{
			LineageOp: types.LineageOp{
				Op:       types.OP_DEL,
				Key:      chunk.Key,
				Id:       chunk.Id,
				Size:     chunk.Size,
				Accessed: chunk.Accessed,
				// Ret: make(chan error, 1),
				Bucket: chunk.Bucket,
			},
			OpRet: types.OpDelayedSuccess(),
			Chunk: chunk,
		}
		s.chanOps <- op
		return op.OpRet
	} else {
		return types.OpSuccess()
	}
}

func (s *PersistentStorage) getBucket(key string) string {
	if Buckets == 1 {
		return ""
	}
	return fmt.Sprintf(s.s3bucket, strconv.FormatUint(xxhash.Sum64([]byte(key))%uint64(Buckets), 10))
}

func (s *PersistentStorage) bucket(b *string) *string {
	if *b == "" {
		return &s.s3bucketDefault
	} else {
		return b
	}
}

func (s *PersistentStorage) ConfigS3(bucket string, prefix string) {
	s.s3bucket = bucket
	s.s3bucketDefault = fmt.Sprintf(bucket, "")
	s.s3prefix = prefix
}

func (s *PersistentStorage) StartTracker() {
	if s.chanOps == nil {
		s.chanOps = make(chan *types.OpWrapper, 10)
		s.signalTracker = make(chan StorageSignal, 1)
		s.trackerStopped = make(chan StorageSignal)
		go s.StartTracker()
		return
	}

	s.log.Debug("Tracking operations...")

	// This is essential to minimize download memory consumption.
	bufferProvider := mys3.NewBufferedReadSeekerWriteToPool(0)

	// Initialize s3 uploader
	smallUploader := s3manager.NewUploader(types.AWSSession(), func(u *s3manager.Uploader) {
		u.Concurrency = 1
		u.BufferProvider = bufferProvider
	})
	largeUploader := s3manager.NewUploader(types.AWSSession(), func(u *s3manager.Uploader) {
		u.Concurrency = types.UploadConcurrency
		u.BufferProvider = bufferProvider
	})

	// Initialize result collector of parallel uploads
	attemps := 3
	persistedOps := make([]*types.OpWrapper, 0, 10)
	persisted := 0
	collectPersisted := func(op *types.OpWrapper) {
		// Fill in the allocated slot.
		persistedOps[op.OpIdx] = op
		// Check persisted yet has not been processed operations.
		for ; persisted < len(persistedOps) && persistedOps[persisted] != nil; persisted++ {
			persistedOp := persistedOps[persisted]
			// Abandon failed uploads.
			if persistedOp.OpRet.Error() == nil {
				s.persistHelper.onPersisted(persistedOp)
			}
		}
	}

	// Token is used as concurrency throttler as well as to accept upload result and keep total ordering.
	freeToken := types.UploadConcurrency
	token := make(chan *types.OpWrapper, freeToken)

	// Signal seen but not processed until all opertions are persisted.
	var delayedSignal StorageSignal

	var trackDuration time.Duration
	var trackStart time.Time
	for {
		select {
		case op := <-s.chanOps:
			// if op == nil {
			// 	// closed
			// 	s.chanOps = nil
			// 	return
			// }

			// Count duration
			if trackStart == (time.Time{}) {
				trackStart = time.Now()
			}

			// Fill token
			if freeToken > 0 {
				freeToken--
				token <- nil
			}

			// Try to get token to continue, if a previously operation persisted, handle that first.
			persistedOp := <-token
			if persistedOp != nil {
				collectPersisted(persistedOp)
			}

			s.log.Debug("Tracking incoming op: %v", op.LineageOp)
			op.OpIdx = len(persistedOps)
			persistedOps = append(persistedOps, nil) // Expand the array and reserve a slot.

			// Upload to s3
			var failure error
			if op.LineageOp.Op == types.OP_SET && !op.Persisted {
				go func() {
					uploadStart := time.Now()
					for i := 0; i < attemps; i++ {
						if i > 0 {
							s.log.Info("Attemp %d - uploading %s ...", i+1, op.Key)
						}

						upParams := &s3manager.UploadInput{
							Bucket: s.bucket(&op.LineageOp.Bucket),
							Key:    aws.String(s.getS3Key(op.LineageOp.Key)),
							Body:   bytes.NewReader(op.Body),
						}
						// Perform an upload.
						uploader := smallUploader
						if int64(len(op.Body)) >= largeUploader.PartSize {
							uploader = largeUploader
						}

						attemptStart := uploadStart
						_, failure = uploader.Upload(upParams)
						if failure != nil {
							s.log.Warn("Attemp %d - failed to upload %s: %v", i+1, op.Key, failure)
						} else {
							// success
							failure = nil
							s.log.Debug("Success to upload %s, upload takes %v, total %v", op.Key, time.Since(attemptStart), time.Since(uploadStart))
							break
						}
					}

					// Success?
					if failure != nil {
						s.log.Error("Failed to upload %s: %v", op.Key, failure)
					}
					op.OpRet.Done(failure)
					token <- op
				}()
			} else {
				op.OpRet.Done()
				token <- op
			}
		case persistedOp := <-token:
			// A operation has been persisted.
			if persistedOp != nil {
				collectPersisted(persistedOp)
			}
			// Refill freeToken
			freeToken++
			// Got enough persisted?
			if delayedSignal != nil && persisted == len(persistedOps) {
				// Retry signal
				s.persistHelper.signal(delayedSignal)
				delayedSignal = nil
			}
		case signal := <-s.persistHelper.getSignalTracker():
			// The tracker will only process after tracked all existing operations.
			// If StorageSignalFlagForceCommit is set, it will be processed immediately.
			forceCommit := signal.Flags()&StorageSignalFlagForceCommit > 0
			if len(s.chanOps) > 0 && !forceCommit {
				// We wait for chanOps get drained.
				s.log.Debug("Found more ops to be persisted, pass and wait for resignal.")
				s.persistHelper.signal(signal)
			} else if persisted < len(persistedOps) && !forceCommit {
				// Wait for being persisted and signalTracker get refilled.
				s.log.Debug("Found ops are persisting, pass and wait for resignal.")
				delayedSignal = signal
			} else {
				if forceCommit {
					s.log.Debug("Force commit after delay, notify who may interest.")
				} else {
					// All operations persisted. Execute signal action
					s.log.Debug("All persisted, notify who may interest.")
				}
				// Count track duration and invalid trackStart
				trackDuration += time.Since(trackStart)
				trackStart = time.Time{}
				// Notify subclasses
				if s.persistHelper.onSignalTracker(signal) {
					// Clean up and stop.
					bufferProvider.Close()
					bufferProvider = nil
					s.chanOps = nil
					s.log.Trace("It took %v to track and persist chunks.", trackDuration)
					return
				}
			}
		}
	}
}

// PersistHelper implementations

func (s *PersistentStorage) onPersisted(persisted *types.OpWrapper) {
	// Default by doing nothing
}

func (s *PersistentStorage) signal(sig StorageSignal) bool {
	if s.signalTracker == nil {
		return false
	}

	select {
	case s.signalTracker <- sig:
		return true
	default:
		<-s.signalTracker // Evict older signal
		s.signalTracker <- sig
		return true
	}
}

func (s *PersistentStorage) getSignalTracker() <-chan StorageSignal {
	return s.signalTracker
}

func (s *PersistentStorage) onSignalTracker(signal StorageSignal) bool {
	select {
	case <-s.trackerStopped:
	default:
		close(s.trackerStopped)
	}
	return true
}

func (s *PersistentStorage) getS3Key(key string) string {
	hash := Hasher.Sum64([]byte(key))
	segmented := string(Segmentor.ReplaceAll([]byte(strconv.FormatUint(hash, 16)), []byte("$0/")))
	return fmt.Sprintf(CHUNK_KEY, s.s3prefix, segmented, key)
}

func (s *PersistentStorage) StopTracker() error {
	// Signal tracker to stop and wait
	if s.persistHelper.signal(PersistentStorageSignal(0)) {
		s.log.Debug("Signal tracker to stop")
		<-s.trackerStopped

		// Clean up
		s.trackerStopped = nil
		s.signalTracker = nil
		if s.s3Downloader != nil {
			s.s3Downloader.Close()
			s.s3Downloader = nil
		}
		s.log.Debug("Operation tracking stopped.")
		return nil
	}

	return ErrTrackerNotStarted
}

func (s *PersistentStorage) getS3Downloader() *mys3.Downloader {
	if s.s3Downloader == nil {
		s.s3Downloader = mys3.NewDownloader(types.AWSSession(), func(d *mys3.Downloader) {
			d.Concurrency = Concurrency
			d.RequestOptions = []awsRequest.Option{
				awsRequest.WithResponseReadTimeout(types.AWSServiceTimeout),
			}
		})
	}
	return s.s3Downloader
}
