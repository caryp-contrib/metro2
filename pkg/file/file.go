// Copyright 2020 The Moov Authors
// Use of this source code is governed by an Apache License
// license that can be found in the LICENSE file.

package file

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"encoding/binary"
	"encoding/json"

	"github.com/moov-io/base/log"
	"github.com/moov-io/metro2/pkg/lib"
	"github.com/moov-io/metro2/pkg/utils"
)

// General file interface
type File interface {
	GetType() string
	SetType(string) error
	SetRecord(lib.Record) error
	AddDataRecord(lib.Record) error
	GetRecord(string) (lib.Record, error)
	GetDataRecords() []lib.Record
	GeneratorTrailer() (lib.Record, error)

	Parse(record []byte) error
	Bytes() []byte
	String(newline bool) string
	Validate() error
}

type headerInformation struct {
	BlockDescriptorWord  int    `json:"blockDescriptorWord"`
	RecordDescriptorWord int    `json:"recordDescriptorWord"`
	RecordIdentifier     string `json:"recordIdentifier"`
}

type dummyFile struct {
	Header *headerInformation `json:"header"`
}

// NewFile constructs a file template.
func NewFile(format string) (File, error) {
	fmt.Fprintf(os.Stdout, "CKP: Entered NewFile\n")
	switch format {
	case utils.CharacterFileFormat:
		fmt.Fprintf(os.Stdout, "CKP: is CharacterFileFormat\n")
		return &fileInstance{
			logger:  log.NewDefaultLogger(),
			format:  utils.CharacterFileFormat,
			Header:  lib.NewHeaderRecord(),
			Trailer: lib.NewTrailerRecord(),
		}, nil
	case utils.PackedFileFormat:
		return &fileInstance{
			logger:  log.NewDefaultLogger(),
			format:  utils.PackedFileFormat,
			Header:  lib.NewPackedHeaderRecord(),
			Trailer: lib.NewPackedTrailerRecord(),
		}, nil
	}
	return nil, utils.NewErrInvalidSegment(format)
}

// NewFileFromReader attempts to parse raw metro2 file or json file
func NewFileFromReader(r io.Reader) (File, error) {
	if r == nil {
		return nil, errors.New("invalid file reader")
	}

	// Take a peek and see if we encounter '{' (would imply the contents is JSON)
	preview := make([]byte, 1024)
	n, err := io.ReadFull(r, preview)
	switch {
	case err == io.ErrUnexpectedEOF:
		preview = preview[:n]
		r = bytes.NewReader(preview)
	case err != nil:
		return nil, err
	default:
		r = io.MultiReader(bytes.NewReader(preview), r)
	}

	// Look for the start of JSON
	var isJSON bool
	for i := range preview {
		if preview[i] == '{' {
			isJSON = true
			break
		}
	}

	fmt.Fprintf(os.Stdout, "CKP: NewFileFromReader: isJson:%t\n", isJSON)

	// Decode contents as Metro2 formatting when it's not JSON
	if !isJSON {
		return NewReader(r).Read()
	}

	fmt.Fprintf(os.Stdout, "CKP: NewFileFromReader: Determine the file format\n")

	// Determine the file format
	var buf bytes.Buffer
	r = io.TeeReader(r, &buf)

	var dummy dummyFile
	err = json.NewDecoder(r).Decode(&dummy)
	if err != nil {
		return nil, fmt.Errorf("reading header: %w", err)
	}

	fileFormat := utils.CharacterFileFormat
	if dummy.Header != nil {
		if dummy.Header.RecordDescriptorWord == lib.UnpackedRecordLength {
			fileFormat = utils.CharacterFileFormat
		} else if dummy.Header.BlockDescriptorWord > 0 {
			fileFormat = utils.PackedFileFormat
		}
	}

	// Decode the file as JSON now
	f, err := NewFile(fileFormat)
	if err != nil {
		return nil, err
	}

	r = io.MultiReader(&buf, r)
	err = json.NewDecoder(r).Decode(f)
	if err != nil {
		return f, fmt.Errorf("reading file: %w", err)
	}
	return f, nil
}

// CreateFile attempts to parse raw metro2 or json
func CreateFile(buf []byte) (File, error) {
	r := bytes.NewReader(buf)
	return NewFileFromReader(r)
}

// Reader reads records from a metro2 encoded file.
type Reader struct {
	// r handles the IO.Reader sent to be parser.
	scanner *bufio.Scanner
	// file is metro2 file model being built as r is parsed.
	File File
	// line is the current line being parsed from the input r
	line []byte
}

// Read reads each record of the metro file
func (r *Reader) Read() (File, error) {
	fmt.Fprintf(os.Stdout, "CKP: Read: Entered function...\n")

	f, ok := r.File.(*fileInstance)
	if !ok {
		return r.File, fmt.Errorf("unexpected File of %T", r.File)
	}

	f.Bases = []lib.Record{}

	// read through the entire file
	if r.scanner.Scan() {
		r.line = r.scanner.Bytes()

		fmt.Fprintf(os.Stdout, "CKP: Read: Header Line: %s\n", r.line)

		// getting file type
		fmt.Fprintf(os.Stdout, "CKP: Read: getting file type\n")
		if !utils.IsMetroFile(r.line) {
			return nil, utils.ErrInvalidMetroFile
		}

		fileFormat := utils.MessageMetroFormat
		if utils.IsPacked(r.line) {
			fileFormat = utils.PackedFileFormat
		}

		f.SetType(fileFormat)
		fmt.Fprintf(os.Stdout, "CKP: Read. File format set:%s\n", fileFormat)

		// Header Record
		fmt.Fprintf(os.Stdout, "CKP: Read. Starting Header parse...\n")
		if _, err := f.Header.Parse(r.line); err != nil {
			fmt.Fprintf(os.Stdout, "CKP: Read. Header parse failed!!\n")
			return nil, err
		}
	} else {
		return nil, utils.NewErrInvalidSegment("header")
	}

	failedParse := false
	for r.scanner.Scan() {
		r.line = r.scanner.Bytes()

		fmt.Fprintf(os.Stdout, "CKP: Read: Base Line: %s\n", r.line)

		var base lib.Record
		if f.format == utils.PackedFileFormat {
			base = lib.NewPackedBaseSegment()
		} else {
			base = lib.NewBaseSegment()
		}
		fmt.Fprintf(os.Stdout, "CKP: Read: Base Format: %s\n", f.format)
		_, err := base.Parse(r.line)
		if err != nil {
			failedParse = true
			return nil, err // return the error
			// break
		}
		f.Bases = append(f.Bases, base)
	}

	fmt.Fprintf(os.Stdout, "CKP: Read: Base Records Parse Success: %t\n", !failedParse)

	// if !failedParse {
	// 	fmt.Fprintf(os.Stdout, "CKP: Read: Start Trailer Scan\n")
	// 	// read new line
	// 	if r.scanner.Scan() {
	// 		r.line = r.scanner.Bytes()
	// 		fmt.Fprintf(os.Stdout, "CKP: Read: Trailer Line: %s\n", r.line)
	// 	} else {
	// 		return nil, utils.NewErrInvalidSegment("Trailer: " + r.scanner.Err().Error())
	// 	}
	// }

	// _, err := f.Trailer.Parse(r.line)
	// if err != nil {
	// 	return nil, err
	// }

	return r.File, nil
}

// NewReader returns a new metro reader that reads from io reader.
func NewReader(r io.Reader) *Reader {
	f, _ := NewFile(utils.CharacterFileFormat)
	fmt.Fprintf(os.Stdout, "CKP: NewReader: new file created.\n")
	reader := &Reader{
		File:    f,
		scanner: bufio.NewScanner(r),
	}
	fmt.Fprintf(os.Stdout, "CKP: NewReader: new reader created.\n")
	reader.scanner.Split(scanRecord)
	fmt.Fprintf(os.Stdout, "CKP: NewReader: Split completed.\n")

	return reader
}

// scanRecord allows reader to split metro file by each record
func scanRecord(data []byte, atEOF bool) (advance int, token []byte, err error) {
	getStripedLength := func() (length int) {
		// Count every byte except for carriage returns and newlines
		for _, b := range data {
			switch b {
			case '\r', '\n':
				continue
			default:
				length += 1
			}
		}
		return length
	}

	getStripedData := func(size int) (int, []byte, error) {
		var buffer bytes.Buffer
		for i := size; i <= len(data); i++ {
			buffer.Reset()
			for _, b := range data[:i] {
				if b != '\r' && b != '\n' {
					buffer.WriteByte(b)
				}
			}
			if buffer.Len() == size {
				return i, buffer.Bytes(), nil
			}
		}
		return 0, nil, io.ErrNoProgress
	}

	length := getStripedLength()

	if atEOF && length == 0 {
		return 0, nil, nil
	} else if length < 4 && atEOF {
		// we ran out of bytes and we're at the end of the file
		return 0, nil, io.ErrUnexpectedEOF
	} else if length < 4 {
		// we need at least the control bytes
		return 0, nil, nil
	}

	_, bdw, _ := getStripedData(4)
	// trying to read for unpacked format
	size, readErr := strconv.ParseInt(string(bdw), 10, 32)
	if readErr == nil {
		if size < lib.UnpackedRecordLength {
			return 0, nil, io.ErrNoProgress
		}
	} else {
		// trying to read for packed format
		size = int64(binary.BigEndian.Uint16(bdw))
		if size < lib.PackedRecordLength {
			return 0, nil, io.ErrNoProgress
		}
	}

	if int(size) <= length {
		// return line while accounting for control bytes
		return getStripedData(int(size))
	} else if int(size) > length && atEOF {
		// we need more data, but there is no more data to read
		return 0, nil, io.ErrUnexpectedEOF
	}

	// request more data.
	return 0, nil, nil
}
