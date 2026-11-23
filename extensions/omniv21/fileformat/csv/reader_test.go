package csv

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/jf-tech/go-corelib/jsons"
	"github.com/jf-tech/go-corelib/strs"
	"github.com/jf-tech/go-corelib/testlib"
	"github.com/stretchr/testify/assert"

	"github.com/jf-tech/omniparser/idr"
)

func TestIsErrInvalidHeader(t *testing.T) {
	assert.True(t, IsErrInvalidHeader(ErrInvalidHeader("test")))
	assert.Equal(t, "test", ErrInvalidHeader("test").Error())
	assert.False(t, IsErrInvalidHeader(errors.New("test")))
}

func TestNewReader_InvalidXPath(t *testing.T) {
	r, err := NewReader("test", nil, nil, "[invalid")
	assert.Error(t, err)
	assert.Equal(t, `invalid xpath '[invalid', err: expression must evaluate to a node-set`, err.Error())
	assert.Nil(t, r)
}

func lf(s string) string {
	return s + "\n"
}

func TestReader(t *testing.T) {
	for _, test := range []struct {
		name     string
		decl     *FileDecl
		xpath    string
		input    io.Reader
		expected []interface{}
	}{
		{
			name: "header row; alias used; with xpath; variable data row column size; replace double quote",
			decl: &FileDecl{
				Delimiter:           "|",
				ReplaceDoubleQuotes: true,
				HeaderRowIndex:      testlib.IntPtr(2),
				DataRowIndex:        4,
				Columns: []Column{
					{Name: "a"},
					{Name: "b with space", Alias: strs.StrPtr("b_with_space")},
					{Name: "c"},
				},
			},
			xpath: ".[a != 'skip']",
			input: strings.NewReader(lf("line1") +
				lf("a|b with space|c") +
				lf("line3") +
				lf(`1|"2|3`) + // put an unescaped double quote here to see if our replacement works or not.
				lf("") + // auto skipped by csv reader.
				lf("skip") + // skipped by the xpath.
				lf("one|two")),
			expected: []interface{}{
				`{ "a": "1", "b_with_space": "'2", "c": "3" }`,
				`{ "a": "one", "b_with_space": "two" }`,
			},
		},
		{
			name: "cannot jump to header row",
			decl: &FileDecl{
				Delimiter:      "|",
				HeaderRowIndex: testlib.IntPtr(3),
			},
			input: strings.NewReader(lf("line1")),
			expected: []interface{}{
				ErrInvalidHeader("input 'test-input' line 2: unable to read header: EOF"),
			},
		},
		{
			name: "cannot read to header row",
			decl: &FileDecl{
				Delimiter:      "|",
				HeaderRowIndex: testlib.IntPtr(2),
			},
			input: strings.NewReader(lf("line1")),
			expected: []interface{}{
				ErrInvalidHeader("input 'test-input' line 2: unable to read header: EOF"),
			},
		},
		{
			name: "header columns less than the declared",
			decl: &FileDecl{
				Delimiter:      "|",
				HeaderRowIndex: testlib.IntPtr(2),
				Columns: []Column{
					{Name: "a"},
					{Name: "b"},
				},
			},
			input: strings.NewReader(lf("line1") + lf("a")),
			expected: []interface{}{
				ErrInvalidHeader("input 'test-input' line 2: actual header column size (1) is less than the size (2) declared in file_declaration.columns in schema"),
			},
		},
		{
			name: "header column not matching the declared",
			decl: &FileDecl{
				Delimiter:      "|",
				HeaderRowIndex: testlib.IntPtr(2),
				Columns: []Column{
					{Name: "a"},
					{Name: "b"},
				},
			},
			input: strings.NewReader(lf("line1") + lf("a|B")),
			expected: []interface{}{
				ErrInvalidHeader("input 'test-input' line 2: header column[2] 'B' does not match declared column name 'b' in schema"),
			},
		},
		{
			name: "header row but no data row",
			decl: &FileDecl{
				Delimiter:      ",",
				HeaderRowIndex: testlib.IntPtr(2),
				DataRowIndex:   5,
				Columns: []Column{
					{Name: "a"},
					{Name: "b"},
				},
			},
			input:    strings.NewReader(lf("line1") + lf("a,b") + lf("line3")),
			expected: nil,
		},
		{
			name: "unable to read data row",
			decl: &FileDecl{
				Delimiter:    ",",
				DataRowIndex: 1,
				Columns:      []Column{{Name: "a"}},
			},
			input: testlib.NewMockReadCloser("read failure", nil),
			expected: []interface{}{
				errors.New("input 'test-input' line 1: failed to fetch record: read failure"),
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, err := NewReader("test-input", test.input, test.decl, test.xpath)
			assert.NoError(t, err)
			for {
				n, err := r.Read()
				if err == io.EOF {
					assert.Equal(t, 0, len(test.expected))
					assert.Nil(t, n)
					break
				}
				assert.True(t, len(test.expected) > 0)
				if expectedErr, ok := test.expected[0].(error); ok {
					assert.Error(t, err)
					assert.Equal(t, expectedErr, err)
					assert.Nil(t, n)
					assert.Equal(t, 1, len(test.expected)) // if there is an error, it will be the last one.
					break
				}
				expectedJSON, ok := test.expected[0].(string)
				assert.True(t, ok)
				assert.Equal(t, jsons.BPJ(expectedJSON), jsons.BPJ(idr.JSONify2(n)))
				r.Release(n)
				test.expected = test.expected[1:]
			}
		})
	}
}

func TestIsContinuableError(t *testing.T) {
	r := &reader{}
	assert.True(t, r.IsContinuableError(errors.New("some error")))
	assert.False(t, r.IsContinuableError(ErrInvalidHeader("invalid header")))
	assert.False(t, r.IsContinuableError(io.EOF))
}

func TestRecordLines_String(t *testing.T) {
	assert.Equal(t, "line 3", RecordLines{Start: 3, End: 3}.String())
	// defensive: a degenerate range collapses to a single line.
	assert.Equal(t, "line 3", RecordLines{Start: 3, End: 2}.String())
	assert.Equal(t, "lines [3,5]", RecordLines{Start: 3, End: 5}.String())
}

func TestRecordLinesOf(t *testing.T) {
	n := idr.CreateNode(idr.DocumentNode, "")
	_, ok := RecordLinesOf(n)
	assert.False(t, ok)
	n.FormatSpecific = RecordLines{Start: 2, End: 4}
	lines, ok := RecordLinesOf(n)
	assert.True(t, ok)
	assert.Equal(t, RecordLines{Start: 2, End: 4}, lines)
}

// TestReader_MultiLineRecords_LineTracking verifies that when quoted fields span
// multiple physical lines, each successfully read record carries its true 1-based
// inclusive line range as trace info (retrievable via RecordLinesOf), that FmtErr
// points at the line range of the record most recently read, and that a bad record
// error points at the real start/end lines of that bad record.
func TestReader_MultiLineRecords_LineTracking(t *testing.T) {
	decl := &FileDecl{
		Delimiter:      ",",
		HeaderRowIndex: testlib.IntPtr(1),
		DataRowIndex:   2,
		Columns: []Column{
			{Name: "name"},
			{Name: "note"},
			{Name: "amount"},
		},
	}
	r, err := NewReader("test-input", strings.NewReader(
		lf("name,note,amount")+ // line 1: header
			lf(`alice,"l1`)+ //    line 2: record 1 starts (quoted field spans lines)
			lf(`l2",100`)+ //      line 3: record 1 ends -> lines [2,3]
			lf("")+ //             line 4: blank line, auto-skipped by csv reader
			lf(`bob,x,200`)+ //    line 5: record 2 -> line 5
			lf(`carol,"p`)+ //     line 6: record 3 starts
			lf(`q`)+ //            line 7
			lf(`r",300`)+ //       line 8: record 3 ends -> lines [6,8]
			lf(`dave,"bad`)+ //    line 9: bad record starts
			lf(`oops"x,400`)+ //   line 10: parse error (junk after closing quote) -> lines [9,10]
			lf(`eve,ok,500`)), //  line 11: record 4 -> line 11
		decl, "")
	assert.NoError(t, err)

	// record 1: multi-line quoted field.
	n, err := r.Read()
	assert.NoError(t, err)
	assert.Equal(t, jsons.BPJ(`{ "name": "alice", "note": "l1\nl2", "amount": "100" }`),
		jsons.BPJ(idr.JSONify2(n)))
	lines, ok := RecordLinesOf(n)
	assert.True(t, ok)
	assert.Equal(t, RecordLines{Start: 2, End: 3}, lines)
	// While this record is being processed (e.g. a transform failure), FmtErr points
	// at the record's real line range.
	assert.Equal(t, "input 'test-input' lines [2,3]: some error", r.FmtErr("some error").Error())
	r.Release(n)

	// record 2: single-line record right after a blank line; the skipped blank line
	// must not be counted into the record's line range.
	n, err = r.Read()
	assert.NoError(t, err)
	lines, ok = RecordLinesOf(n)
	assert.True(t, ok)
	assert.Equal(t, RecordLines{Start: 5, End: 5}, lines)
	assert.Equal(t, "input 'test-input' line 5: some error", r.FmtErr("some error").Error())
	r.Release(n)

	// record 3: quoted field spanning 3 physical lines.
	n, err = r.Read()
	assert.NoError(t, err)
	assert.Equal(t, jsons.BPJ(`{ "name": "carol", "note": "p\nq\nr", "amount": "300" }`),
		jsons.BPJ(idr.JSONify2(n)))
	lines, ok = RecordLinesOf(n)
	assert.True(t, ok)
	assert.Equal(t, RecordLines{Start: 6, End: 8}, lines)
	r.Release(n)

	// bad record: parse error inside a multi-line quoted field. The error points at
	// the real start/end lines of the bad record, and is continuable.
	n, err = r.Read()
	assert.Error(t, err)
	assert.Nil(t, n)
	assert.Contains(t, err.Error(), "input 'test-input' lines [9,10]: failed to fetch record: ")
	assert.Contains(t, err.Error(), "record on line 9")
	assert.True(t, r.IsContinuableError(err))

	// record 4: reading continues after the bad record.
	n, err = r.Read()
	assert.NoError(t, err)
	lines, ok = RecordLinesOf(n)
	assert.True(t, ok)
	assert.Equal(t, RecordLines{Start: 11, End: 11}, lines)
	r.Release(n)

	// EOF.
	n, err = r.Read()
	assert.Equal(t, io.EOF, err)
	assert.Nil(t, n)
}
