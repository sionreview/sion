package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path"
	"regexp"
	"strings"

	nanoReader "github.com/ScottMansfield/nanolog/reader"
	"github.com/sionreview/sion/common/logger"
)

var (
	log = &logger.ColorLogger{Color: true, Level: logger.LOG_LEVEL_ALL}

	// No.1_lambda2048_40_100_500
	fileNameRecognizer      = regexp.MustCompile(`No.(\d+)_lambda(\d+)_(\d+)_(\d+)_(\d+)`)
	analyticsTaskRecognizer = regexp.MustCompile(`(map|reduce)_io_data_([a-zA-Z]+)\d+\.dat`)
	analyticsLineRecognizer = regexp.MustCompile(`^\{(.+?)\}$`)
	functionRecognizer      *regexp.Regexp

	recoveryFilenameMatcher = regexp.MustCompile(`^\d+$`)
)

// Options Options definition
type Options struct {
	path            string
	output          string
	merge           bool
	processor       string
	fileFilter      string
	fileMatcher     *regexp.Regexp
	lineFilter      string
	lineMatcher     *regexp.Regexp
	lastLineFilter  string
	lastLineMatcher *regexp.Regexp
	functionPrefix  string
}

func main() {
	options := &Options{}
	checkUsage(options)

	flags := os.O_CREATE | os.O_WRONLY
	if options.merge {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(options.output, flags, 0644)
	if err != nil {
		panic(err)
		return
	}
	defer file.Close()

	all := make(chan string, 1)
	go func() {
		root, err := os.Stat(options.path)
		if err != nil {
			log.Error("Error on get stat %s: %v", options.path, err)
		} else if !root.IsDir() {
			log.Info("Collecting %s", options.path)
			all <- options.path
		} else if err := iterateDir(options.path, options.fileMatcher, all); err != nil {
			log.Error("Error on iterating path: %v", err)
		}

		close(all)
	}()

	collectAll(all, file, options)
}

func iterateDir(root string, filter *regexp.Regexp, collectors ...chan string) error {
	log.Debug("Reading %s", root)
	files, err := ioutil.ReadDir(root)
	if err != nil {
		return err
	}

	for _, file := range files {
		full := path.Join(root, file.Name())
		if file.IsDir() {
			iterateDir(full, filter, collectors...)
		} else if filter == nil || filter.MatchString(file.Name()) {
			log.Info("Collecting %s", full)
			for _, collector := range collectors {
				collector <- full
			}
		}
	}

	return nil
}

func collectAll(dataFiles chan string, file io.Writer, opts *Options) {
	writeTitle(file, opts)
	for df := range dataFiles {
		var err error
		switch opts.processor {
		case "csv":
			prepend := strings.Join(fileNameRecognizer.FindStringSubmatch(df)[1:], ",")
			err = csvProcessor(df, file, prepend, opts)
		case "nanolog":
			prepend := strings.Join(fileNameRecognizer.FindStringSubmatch(df)[1:], ",")
			err = nanologProcessor(df, file, prepend, opts)
		case "recovery":
			prepend := strings.Join(fileNameRecognizer.FindStringSubmatch(df)[1:], ",")
			err = recoveryProcessor(df, file, prepend, opts)
		case "workload":
			// fi, err := os.Stat(df)
			// if err != nil {
			// 	log.Warn("Failed to get stat of file %s: %v", df, err)
			// 	continue
			// }
			// stat_t := fi.Sys().(*syscall.Stat_t)
			// ct := time.Unix(int64(stat_t.Mtimespec.Sec), int64(0)) // Mac
			// ct := time.Unix(int64(stat_t.Ctim.Sec), int64(0)) // Linux
			// prepend := ct.UTC().String()
			prepend := ""
			if functionRecognizer != nil {
				matched := functionRecognizer.FindStringSubmatch(df)
				if len(matched) > 0 {
					prepend = matched[0]
				}
			}
			err = recoveryProcessor(df, file, prepend, opts)
		case "analytics":
			matched := analyticsTaskRecognizer.FindStringSubmatch(df)
			prepend := ""
			if len(matched) > 0 {
				prepend = strings.Join(matched[1:], ",")
			}
			err = dataAnlyticsProcessor(df, file, prepend, opts)
		default:
			log.Error("Unsupported processor: %s", opts.processor)
			return
		}
		if err != nil {
			log.Warn("Failed to process %s: %v", df, err)
		}
	}
}

func writeTitle(f io.Writer, opts *Options) {
	if opts.processor == "recovery" {
		io.WriteString(f, "no,mem,numbackups,objsize,interval,time,op,recovery,node,backey,lineage,objects,total,lineagesize,objectsize,numobjects,session\n")
	} else if opts.processor == "workload" {
		io.WriteString(f, "func,time,op,recovery,node,backey,lineage,objects,total,lineagesize,objectsize,numobjects,session\n")
	} else if opts.processor == "analytics" {
		io.WriteString(f, "op,task,taskid,taskkey,size,start,end\n")
	}
}

func checkUsage(options *Options) {
	var printInfo bool
	flag.BoolVar(&printInfo, "h", false, "help info?")

	flag.StringVar(&options.output, "o", "exp.csv", "Filename of merged data.")
	flag.BoolVar(&options.merge, "a", false, "Append to output if possible.")
	flag.StringVar(&options.processor, "processor", "csv", "Function to process one file: csv, nanolog, recovery.")
	flag.StringVar(&options.fileFilter, "filter-files", "", "Regexp to filter files to process.")
	flag.StringVar(&options.lineFilter, "filter-lines", "", "Regexp to filter lines to output.")
	flag.StringVar(&options.lastLineFilter, "filter-previous-line", "", "Regexp to filter lines that has specified previous line to output.")
	flag.StringVar(&options.functionPrefix, "fprefix", "", "Regexp for function recognition.")

	flag.Parse()

	if printInfo || flag.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Usage: ./preprocess [options] data_base_path\n")
		fmt.Fprintf(os.Stderr, "Available options:\n")
		flag.PrintDefaults()
		os.Exit(0)
	}

	options.path = flag.Arg(0)

	if options.fileFilter != "" {
		options.fileMatcher = regexp.MustCompile(options.fileFilter)
		log.Info("Will filter files matching %v", options.fileMatcher)
	} else if options.processor == "recovery" {
		options.fileMatcher = recoveryFilenameMatcher
	}

	if options.lineFilter != "" {
		options.lineMatcher = regexp.MustCompile(options.lineFilter)
		log.Info("Will filter lines matching %v", options.lineMatcher)
	}

	if options.lastLineFilter != "" {
		options.lastLineMatcher = regexp.MustCompile(options.lastLineFilter)
		log.Info("Will filter last line matching %v", options.lastLineMatcher)
	}

	if options.functionPrefix != "" {
		functionRecognizer = regexp.MustCompile(fmt.Sprintf("%s(\\d+)", options.functionPrefix))
		log.Info("Will recognize function name like %v", functionRecognizer)
	}
}

func csvProcessor(df string, file io.Writer, prepend string, opts *Options) error {
	dfile, err := ioutil.ReadFile(df)
	if err != nil {
		return err
	}
	// Normalize
	lines := strings.Split(string(dfile), "\n")
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		io.WriteString(file, prepend)
		io.WriteString(file, ",")
		io.WriteString(file, line)
		io.WriteString(file, "\n")
	}
	return nil
}

func dataAnlyticsProcessor(df string, file io.Writer, prepend string, opts *Options) error {
	dfile, err := os.Open(df)
	if err != nil {
		return err
	}
	defer dfile.Close()

	s := bufio.NewScanner(dfile)
	s.Split(bufio.ScanLines)
	for s.Scan() {
		line := s.Text()
		// matches := analyticsLineRecognizer.FindStringSubmatch(line)
		// if len(matches) == 0 {
		// 	continue
		// }
		// line = matches[1]
		line = line[1 : len(line)-1]

		io.WriteString(file, prepend)
		io.WriteString(file, ",")
		segments := strings.Split(line, " ")
		if len(segments) == 1 {
			segments = strings.Split(line, "\t")
		}
		io.WriteString(file, strings.Join(segments, ","))
		io.WriteString(file, "\n")
	}
	return nil
}

func recoveryProcessor(df string, file io.Writer, prepend string, opts *Options) error {
	dfile, err := ioutil.ReadFile(df)
	if err != nil {
		return err
	}
	// Normalize
	lines := strings.Split(string(dfile), "\n")
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		fields := strings.Split(line, ",")
		for len(fields) >= 12 {
			l := strings.Join(fields[:11], ",")
			if len(prepend) > 0 {
				io.WriteString(file, prepend)
				io.WriteString(file, ",")
			}
			io.WriteString(file, l)
			io.WriteString(file, ",")
			if len(fields[11]) > 36 {
				io.WriteString(file, fields[11][:36])
				io.WriteString(file, "\n")
				fields[10] = fields[11][36:]
				fields = fields[11:]
			} else {
				io.WriteString(file, fields[11])
				io.WriteString(file, "\n")
				fields = fields[12:]
			}
		}
		if len(fields) > 0 {
			log.Warn("Unexepected remains in %s: %v", df, fields)
		}
	}
	return nil
}

func nanologProcessor(df string, file io.Writer, prepend string, opts *Options) error {
	dfile, err := os.Open(df)
	if err != nil {
		return err
	}
	defer dfile.Close()

	reader, writer := io.Pipe()
	go func() {
		if err := nanoReader.New(dfile, writer).Inflate(); err != nil {
			log.Error("Failed to inflate %s: %v", df, err)
		}
		writer.Close()
	}()

	s := bufio.NewScanner(reader)
	s.Split(bufio.ScanLines)
	lastLine := ""
	for s.Scan() {
		line := s.Text()
		last := lastLine
		lastLine = line

		if len(line) == 0 {
			continue
		}
		if opts.lineMatcher != nil && !opts.lineMatcher.Match([]byte(line)) {
			continue
		}
		if opts.lastLineMatcher != nil && !opts.lastLineMatcher.Match([]byte(last)) {
			continue
		}
		if len(prepend) > 0 {
			io.WriteString(file, prepend)
			io.WriteString(file, ",")
		}
		io.WriteString(file, line)
		io.WriteString(file, "\n")
	}
	return nil
}
