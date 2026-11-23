package csv

import (
	stdcsv "encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/antchfx/xpath"
	"github.com/jf-tech/go-corelib/caches"
	"github.com/jf-tech/go-corelib/ios"
	"github.com/jf-tech/go-corelib/maths"

	"github.com/jf-tech/omniparser/idr"
)

// RecordLines describes the 1-based, inclusive physical line range a CSV record occupies
// in the input file. A record can span multiple physical lines when it contains quoted
// fields with embedded newlines, in which case Start < End.
//
// The RecordLines of a successfully ingested record is attached to the record's IDR root
// node (in idr.Node.FormatSpecific) as trace info, so the record can be traced back to,
// and re-read from, the original input file. It is also used to point error messages at
// the real line range of a bad record.
type RecordLines struct {
	Start int // 1-based line number of the first physical line of the record.
	End   int // 1-based line number of the last physical line of the record, inclusive.
}

// String returns a human readable representation of the line range, collapsing it into
// a single "line N" when the record occupies only one physical line.
func (l RecordLines) String() string {
	if l.End <= l.Start {
		return fmt.Sprintf("line %d", l.Start)
	}
	return fmt.Sprintf("lines [%d,%d]", l.Start, l.End)
}

// RecordLinesOf returns the RecordLines trace info attached to the IDR root node of a
// record previously returned by the CSV reader. The second return value indicates
// whether such trace info is present on the node.
func RecordLinesOf(n *idr.Node) (RecordLines, bool) {
	lines, ok := n.FormatSpecific.(RecordLines)
	return lines, ok
}

// ErrInvalidHeader indicates the header of the CSV input is corrupted, mismatched, or simply
// unreadable. This is a fatal, non-continuable error.
type ErrInvalidHeader string

func (e ErrInvalidHeader) Error() string { return string(e) }

// IsErrInvalidHeader checks if the `err` is of ErrInvalidHeader type.
func IsErrInvalidHeader(err error) bool {
	switch err.(type) {
	case ErrInvalidHeader:
		return true
	default:
		return false
	}
}

type reader struct {
	inputName     string
	decl          *FileDecl
	xpath         *xpath.Expr
	r             *ios.LineNumReportingCsvReader
	headerChecked bool
	// lastRecordLines is the line range of the record most recently read from the
	// input. It's nil before the first record is read (e.g. during header processing).
	lastRecordLines *RecordLines
}

func (r *reader) Read() (*idr.Node, error) {
	if !r.headerChecked {
		err := r.checkHeader()
		r.headerChecked = true
		if err != nil {
			return nil, err
		}
	}
read:
	record, err := r.r.Read()
	if err == io.EOF {
		return nil, io.EOF
	}
	if err != nil {
		return nil, r.fmtErr(r.errRecordLines(err), "failed to fetch record: %s", err.Error())
	}
	lines := r.recordLines(record)
	r.lastRecordLines = &lines
	n := r.recordToNode(record)
	n.FormatSpecific = lines
	if r.xpath != nil && !idr.MatchAny(n, r.xpath) {
		goto read
	}
	return n, nil
}

// recordLines computes the 1-based inclusive physical line range a just-read record
// occupies in the input. After a successful Read, r.r.LineNum() points at the record's
// last physical line. A record spans multiple physical lines only when its quoted fields
// embed newlines, and those newlines are preserved in the parsed field values, so the
// number of physical lines the record covers is 1 plus the total number of newlines
// across all its fields. Deriving the start line this way (instead of using the previous
// record's end line + 1) also correctly skips over blank lines that the csv reader
// auto-skips before the record.
func (r *reader) recordLines(record []string) RecordLines {
	end := r.r.LineNum()
	span := 1
	for _, field := range record {
		span += strings.Count(field, "\n")
	}
	return RecordLines{Start: end - span + 1, End: end}
}

// errRecordLines computes the line range of a record whose read failed. A csv.ParseError
// already carries the true start/end lines of the offending record; for any other error
// (e.g. an underlying IO failure), the best we know is the line the reader was at when
// the failure occurred.
func (r *reader) errRecordLines(err error) RecordLines {
	var parseErr *stdcsv.ParseError
	if errors.As(err, &parseErr) {
		return RecordLines{Start: parseErr.StartLine, End: parseErr.Line}
	}
	line := r.r.LineNum()
	if line < 1 {
		line = 1
	}
	return RecordLines{Start: line, End: line}
}

func (r *reader) checkHeader() error {
	var err error
	var header []string
	if r.decl.HeaderRowIndex == nil {
		goto skipToDataRow
	}
	err = r.jumpTo(*r.decl.HeaderRowIndex - 1)
	if err != nil {
		return ErrInvalidHeader(r.fmtErrStr(r.currentLines(), "unable to read header: %s", err.Error()))
	}
	header, err = r.r.Read()
	if err != nil {
		return ErrInvalidHeader(r.fmtErrStr(r.currentLines(), "unable to read header: %s", err.Error()))
	}
	if len(header) < len(r.decl.Columns) {
		return ErrInvalidHeader(r.fmtErrStr(r.currentLines(),
			"actual header column size (%d) is less than the size (%d) declared in file_declaration.columns in schema",
			len(header), len(r.decl.Columns)))
	}
	for index, column := range r.decl.Columns {
		if strings.TrimSpace(header[index]) != strings.TrimSpace(column.Name) {
			return ErrInvalidHeader(r.fmtErrStr(r.currentLines(),
				"header column[%d] '%s' does not match declared column name '%s' in schema",
				index+1, strings.TrimSpace(header[index]), strings.TrimSpace(column.Name)))
		}
	}
skipToDataRow:
	if err = r.jumpTo(r.decl.DataRowIndex - 1); err != nil {
		return err
	}
	return nil
}

// the only possible error this jumpTo returns is io.EOF. if there is any reading error, we'll ignore
// because we really don't care about what's corrupted in a line. Now it's possible, but very very
// rarely, that the input reader's underlying media fails to read due memory/disk/IO issue. Since we
// can't reliably tease apart those failures from a simple line corruption failure, we'll choose to
// ignore them equally. And those underlying media failures most likely will repeat and cause subsequent
// read to fail, and then the reader will fail out entirely.
func (r *reader) jumpTo(rowIndex int) error {
	for r.r.LineNum() < rowIndex {
		_, err := r.r.Read()
		if err == io.EOF {
			return io.EOF
		}
	}
	return nil
}

func (r *reader) recordToNode(record []string) *idr.Node {
	root := idr.CreateNode(idr.DocumentNode, "")
	// - If actual record has more columns than declared in schema, we'll only use up to
	//   what's declared in the schema;
	// - conversely, if the actual record has fewer columns than declared in schema, we'll
	//   use all that are in the record.
	for i := 0; i < maths.MinInt(len(record), len(r.decl.Columns)); i++ {
		col := idr.CreateNode(idr.ElementNode, r.decl.Columns[i].name())
		idr.AddChild(root, col)
		data := idr.CreateNode(idr.TextNode, record[i])
		idr.AddChild(col, data)
	}
	return root
}

func (r *reader) Release(n *idr.Node) {
	if n != nil {
		idr.RemoveAndReleaseTree(n)
	}
}

func (r *reader) IsContinuableError(err error) bool {
	return !IsErrInvalidHeader(err) && err != io.EOF
}

func (r *reader) FmtErr(format string, args ...interface{}) error {
	return r.fmtErr(r.currentLines(), format, args...)
}

// currentLines returns the line range of the record most recently read from the input;
// if no record has been read yet (e.g. an error occurs during header processing), it
// returns the reader's current line.
func (r *reader) currentLines() RecordLines {
	if r.lastRecordLines != nil {
		return *r.lastRecordLines
	}
	line := r.r.LineNum()
	return RecordLines{Start: line, End: line}
}

func (r *reader) fmtErr(lines RecordLines, format string, args ...interface{}) error {
	return errors.New(r.fmtErrStr(lines, format, args...))
}

func (r *reader) fmtErrStr(lines RecordLines, format string, args ...interface{}) string {
	return fmt.Sprintf("input '%s' %s: %s", r.inputName, lines.String(), fmt.Sprintf(format, args...))
}

// NewReader creates an FormatReader for CSV file format.
func NewReader(inputName string, r io.Reader, decl *FileDecl, xpathStr string) (*reader, error) {
	var expr *xpath.Expr
	var err error
	xpathStr = strings.TrimSpace(xpathStr)
	if xpathStr != "" && xpathStr != "." {
		expr, err = caches.GetXPathExpr(xpathStr)
		if err != nil {
			return nil, fmt.Errorf("invalid xpath '%s', err: %s", xpathStr, err.Error())
		}
	}
	if decl.ReplaceDoubleQuotes {
		r = ios.NewBytesReplacingReader(r, []byte(`"`), []byte(`'`))
	}
	csv := ios.NewLineNumReportingCsvReader(r)
	delim := []rune(decl.Delimiter)
	csv.Comma = delim[0]
	csv.FieldsPerRecord = -1
	csv.ReuseRecord = true
	return &reader{
		inputName:     inputName,
		decl:          decl,
		r:             csv,
		headerChecked: false,
		xpath:         expr,
	}, nil
}
