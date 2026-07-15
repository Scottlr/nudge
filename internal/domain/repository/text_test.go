package repository

import (
	"io"
	"testing"
)

func TestClassifyTextBytesPreservesExactLineEndingEvidence(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		encoding TextEncodingClass
		lf, crlf uint64
		cr       uint64
		finalLF  bool
		mixed    bool
		empty    bool
	}{
		{name: "lf", data: []byte("one\ntwo\n"), encoding: TextUTF8, lf: 2, finalLF: true},
		{name: "crlf", data: []byte("one\r\ntwo\r\n"), encoding: TextUTF8, crlf: 2, finalLF: true},
		{name: "mixed", data: []byte("one\r\ntwo\nthree\r"), encoding: TextUTF8, lf: 1, crlf: 1, cr: 1, mixed: true},
		{name: "bom", data: []byte{0xef, 0xbb, 0xbf, 'x'}, encoding: TextUTF8BOM},
		{name: "empty", data: nil, encoding: TextUTF8, empty: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			semantics, err := ClassifyTextBytes(test.data)
			if err != nil {
				t.Fatal(err)
			}
			if semantics.Encoding != test.encoding || semantics.Endings.LFCount != test.lf || semantics.Endings.CRLFCount != test.crlf || semantics.Endings.CRCount != test.cr || semantics.Endings.FinalLF != test.finalLF || semantics.Endings.Mixed != test.mixed || semantics.Empty != test.empty || semantics.ByteLength != uint64(len(test.data)) {
				t.Fatalf("semantics = %#v", semantics)
			}
		})
	}
}

func TestClassifyTextReaderHandlesSplitUTF8AndRejectsInvalid(t *testing.T) {
	valid, err := ClassifyTextReader(&chunkReader{data: []byte("a€\r\nb")}, 7)
	if err != nil || valid.Encoding != TextUTF8 || valid.Endings.CRLFCount != 1 {
		t.Fatalf("valid semantics = %#v, error = %v", valid, err)
	}
	invalid, err := ClassifyTextBytes([]byte{'a', 0xff})
	if err != nil || invalid.Encoding != TextEncodingUnknown {
		t.Fatalf("invalid semantics = %#v, error = %v", invalid, err)
	}
}

func TestProjectTextLinesKeepsRawRangesAndCRLFDisplay(t *testing.T) {
	data := []byte("one\r\ntwo\rthree")
	lines, err := ProjectTextLines(data)
	if err != nil || len(lines) != 3 {
		t.Fatalf("lines/error = %#v/%v", lines, err)
	}
	want := []TextLineProjection{
		{RawStart: 0, RawEnd: 5, Terminator: TerminatorCRLF, DisplayText: "one"},
		{RawStart: 5, RawEnd: 9, Terminator: TerminatorCR, DisplayText: "two"},
		{RawStart: 9, RawEnd: 14, Terminator: TerminatorNone, DisplayText: "three"},
	}
	for index := range want {
		if lines[index] != want[index] {
			t.Fatalf("line %d = %#v, want %#v", index, lines[index], want[index])
		}
	}
}

type chunkReader struct {
	data []byte
	read int
}

func (r *chunkReader) Read(output []byte) (int, error) {
	if r.read == len(r.data) {
		return 0, io.EOF
	}
	amount := 1
	if amount > len(output) {
		amount = len(output)
	}
	output[0] = r.data[r.read]
	r.read += amount
	if r.read == len(r.data) {
		return amount, nil
	}
	return amount, nil
}
