package raft

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"testing"
)

// Log with 10 entries with terms as shown in Figure 7, leader line
func makeLogTerms_Figure7LeaderLine() []TermNo {
	return []TermNo{1, 1, 1, 4, 4, 5, 5, 6, 6, 6}
}

// Helper
func testCommandEquals(c Command, s string) bool {
	return bytes.Equal(c, Command(s))
}

// Blackbox test
// Send a Log with 10 entries with terms as shown in Figure 7, leader line.
// No commands should have been applied yet.
func PartialTest_Log_BlackboxTest(t *testing.T, lasm LogAndStateMachine) {
	// Initial data tests
	iole, err := lasm.GetIndexOfLastEntry()
	if err != nil {
		t.Fatal()
	}
	if iole != 10 {
		t.Fatal()
	}
	term, err := lasm.GetTermAtIndex(10)
	if err != nil {
		t.Fatal(err)
	}
	if term != 6 {
		t.Fatal()
	}

	// get entries test
	le := testHelper_GetLogEntryAtIndex(lasm, 10)
	if le.TermNo != 6 {
		t.Fatal(le.TermNo)
	}
	if !testCommandEquals(le.Command, "c10") {
		t.Fatal(le.Command)
	}

	var logEntries []LogEntry

	// set test - invalid index
	logEntries = []LogEntry{{8, Command("c12")}}
	err = lasm.SetEntriesAfterIndex(11, logEntries)
	if err == nil {
		t.Fatal()
	}

	// set test - no replacing
	logEntries = []LogEntry{{7, Command("c11")}, {8, Command("c12")}}
	err = lasm.SetEntriesAfterIndex(10, logEntries)
	if err != nil {
		t.Fatal()
	}
	iole, err = lasm.GetIndexOfLastEntry()
	if err != nil {
		t.Fatal()
	}
	if iole != 12 {
		t.Fatal()
	}
	le = testHelper_GetLogEntryAtIndex(lasm, 12)
	if !reflect.DeepEqual(le, LogEntry{8, Command("c12")}) {
		t.Fatal(le)
	}

	// set test - partial replacing
	logEntries = []LogEntry{{7, Command("c11")}, {9, Command("c12")}, {9, Command("c13'")}}
	err = lasm.SetEntriesAfterIndex(10, logEntries)
	if err != nil {
		t.Fatal()
	}
	iole, err = lasm.GetIndexOfLastEntry()
	if err != nil {
		t.Fatal()
	}
	if iole != 13 {
		t.Fatal()
	}
	le = testHelper_GetLogEntryAtIndex(lasm, 12)
	if !reflect.DeepEqual(le, LogEntry{9, Command("c12")}) {
		t.Fatal(le)
	}

	// append test
	result, err := lasm.AppendEntry(8, "c14")
	if err != nil {
		t.Fatal(err)
	}
	if result != inMemoryLog_AppendEntry_Ok {
		t.Fatal(result)
	}
	if !reflect.DeepEqual(result, inMemoryLog_AppendEntry_Ok) {
		t.Fatal(result)
	}
	iole, err = lasm.GetIndexOfLastEntry()
	if err != nil {
		t.Fatal()
	}
	if iole != 14 {
		t.Fatal()
	}
	le = testHelper_GetLogEntryAtIndex(lasm, 14)
	if !reflect.DeepEqual(le, LogEntry{8, Command("c14")}) {
		t.Fatal(le)
	}

	// commitIndex tests
	err = lasm.CommitIndexChanged(1)
	if err != nil {
		t.Fatal(err)
	}
	err = lasm.CommitIndexChanged(3)
	if err != nil {
		t.Fatal(err)
	}
	err = lasm.CommitIndexChanged(2)
	if err == nil {
		t.Fatal()
	}

	// set test - no new entries with empty slice
	logEntries = []LogEntry{}
	err = lasm.SetEntriesAfterIndex(3, logEntries)
	if err != nil {
		t.Fatal()
	}
	iole, err = lasm.GetIndexOfLastEntry()
	if err != nil {
		t.Fatal()
	}
	if iole != 3 {
		t.Fatal()
	}
	le = testHelper_GetLogEntryAtIndex(lasm, 3)
	if !reflect.DeepEqual(le, LogEntry{1, Command("c3")}) {
		t.Fatal(le)
	}

	// commitIndex test - error to go past end of log
	err = lasm.CommitIndexChanged(4)
	if err == nil {
		t.Fatal()
	}

	// set test - error to modify log before commitIndex
	err = lasm.SetEntriesAfterIndex(2, []LogEntry{})
	if err == nil {
		t.Fatal()
	}
}

// In-memory implementation of LogEntries - meant only for tests
type inMemoryLog struct {
	entries                  []LogEntry
	_commitIndex             LogIndex
	maxEntriesPerAppendEntry uint64
}

func (imle *inMemoryLog) GetIndexOfLastEntry() (LogIndex, error) {
	return LogIndex(len(imle.entries)), nil
}

func (imle *inMemoryLog) GetTermAtIndex(li LogIndex) (TermNo, error) {
	if li == 0 {
		return 0, errors.New("GetTermAtIndex(): li=0")
	}
	if li > LogIndex(len(imle.entries)) {
		return 0, fmt.Errorf(
			"GetTermAtIndex(): li=%v > iole=%v", li, len(imle.entries),
		)
	}
	return imle.entries[li-1].TermNo, nil
}

func (imle *inMemoryLog) SetEntriesAfterIndex(
	li LogIndex,
	entries []LogEntry,
) error {
	if li < imle._commitIndex {
		return fmt.Errorf(
			"inMemoryLog: setEntriesAfterIndex(%d, ...) but commitIndex=%d",
			li,
			imle._commitIndex,
		)
	}
	iole, err := imle.GetIndexOfLastEntry()
	if err != nil {
		return err
	}
	if iole < li {
		return fmt.Errorf("inMemoryLog: setEntriesAfterIndex(%d, ...) but iole=%d", li, iole)
	}
	// delete entries after index
	if iole > li {
		imle.entries = imle.entries[:li]
	}
	// append entries
	imle.entries = append(imle.entries, entries...)
	return nil
}

func (imle *inMemoryLog) AppendEntry(termNo TermNo, rawCommand interface{}) (interface{}, error) {
	switch rawCommand := rawCommand.(type) {
	case string:
		entry := LogEntry{termNo, []byte(rawCommand)}
		imle.entries = append(imle.entries, entry)
	default:
		// FIXME: test
		return fmt.Errorf("oops! rawCommand %#v of unknown type: %T", rawCommand, rawCommand), nil
	}
	return inMemoryLog_AppendEntry_Ok, nil
}

const (
	inMemoryLog_AppendEntry_Ok = "We accept your offering!"
)

func (imle *inMemoryLog) GetEntriesAfterIndex(
	afterLogIndex LogIndex,
) ([]LogEntry, error) {
	iole, err := imle.GetIndexOfLastEntry()
	if err != nil {
		return nil, err
	}

	if iole < afterLogIndex {
		return nil, fmt.Errorf(
			"afterLogIndex=%v is > iole=%v",
			afterLogIndex,
			iole,
		)
	}

	var numEntriesToGet uint64 = uint64(iole - afterLogIndex)

	// Short-circuit allocation for common case
	if numEntriesToGet == 0 {
		return []LogEntry{}, nil
	}

	if numEntriesToGet > imle.maxEntriesPerAppendEntry {
		numEntriesToGet = imle.maxEntriesPerAppendEntry
	}

	logEntries := make([]LogEntry, numEntriesToGet)
	var i uint64 = 0
	nextIndexToGet := afterLogIndex + 1

	for i < numEntriesToGet {
		logEntries[i] = imle.entries[nextIndexToGet-1]
		i++
		nextIndexToGet++
	}

	return logEntries, nil
}

func (imle *inMemoryLog) CommitIndexChanged(commitIndex LogIndex) error {
	if commitIndex < imle._commitIndex {
		return fmt.Errorf(
			"inMemoryLog: CommitIndexChanged(%d) is < current commitIndex=%d",
			commitIndex,
			imle._commitIndex,
		)
	}
	iole, err := imle.GetIndexOfLastEntry()
	if err != nil {
		return err
	}
	if commitIndex > iole {
		return fmt.Errorf(
			"inMemoryLog: CommitIndexChanged(%d) is > iole=%d",
			commitIndex,
			iole,
		)
	}
	imle._commitIndex = commitIndex
	return nil
}

func newIMLEWithDummyCommands(
	logTerms []TermNo,
	maxEntriesPerAppendEntry uint64,
) *inMemoryLog {
	if maxEntriesPerAppendEntry <= 0 {
		panic("maxEntriesPerAppendEntry must be greater than zero")
	}
	entries := []LogEntry{}
	for i, term := range logTerms {
		entries = append(entries, LogEntry{term, Command("c" + strconv.Itoa(i+1))})
	}
	imle := &inMemoryLog{
		entries,
		0,
		maxEntriesPerAppendEntry,
	}
	return imle
}

// Run the blackbox test on inMemoryLog
func TestInMemoryLogEntries(t *testing.T) {
	// Log with 10 entries with terms as shown in Figure 7, leader line
	terms := makeLogTerms_Figure7LeaderLine()
	imle := newIMLEWithDummyCommands(terms, 3)
	PartialTest_Log_BlackboxTest(t, imle)
}

// Tests for inMemoryLog's GetEntriesAfterIndex implementation
func TestIMLE_GetEntriesAfterIndex(t *testing.T) {
	// Log with 10 entries with terms as shown in Figure 7, leader line
	terms := makeLogTerms_Figure7LeaderLine()
	imle := newIMLEWithDummyCommands(terms, 3)

	// none
	actualEntries, err := imle.GetEntriesAfterIndex(10)
	if err != nil {
		t.Fatal()
	}
	expectedEntries := []LogEntry{}
	if !reflect.DeepEqual(actualEntries, expectedEntries) {
		t.Fatal(actualEntries)
	}

	// one
	actualEntries, err = imle.GetEntriesAfterIndex(9)
	if err != nil {
		t.Fatal()
	}
	expectedEntries = []LogEntry{
		{6, Command("c10")},
	}
	if !reflect.DeepEqual(actualEntries, expectedEntries) {
		t.Fatal(actualEntries)
	}

	// multiple
	actualEntries, err = imle.GetEntriesAfterIndex(7)
	if err != nil {
		t.Fatal()
	}
	expectedEntries = []LogEntry{
		{6, Command("c8")},
		{6, Command("c9")},
		{6, Command("c10")},
	}
	if !reflect.DeepEqual(actualEntries, expectedEntries) {
		t.Fatal(actualEntries)
	}

	// max
	actualEntries, err = imle.GetEntriesAfterIndex(2)
	if err != nil {
		t.Fatal()
	}
	expectedEntries = []LogEntry{
		{1, Command("c3")},
		{4, Command("c4")},
		{4, Command("c5")},
	}
	if !reflect.DeepEqual(actualEntries, expectedEntries) {
		t.Fatal(actualEntries)
	}

	// index of 0
	actualEntries, err = imle.GetEntriesAfterIndex(0)
	if err != nil {
		t.Fatal()
	}
	expectedEntries = []LogEntry{
		{1, Command("c1")},
		{1, Command("c2")},
		{1, Command("c3")},
	}
	if !reflect.DeepEqual(actualEntries, expectedEntries) {
		t.Fatal(actualEntries)
	}

	// max alternate value
	imle.maxEntriesPerAppendEntry = 2
	actualEntries, err = imle.GetEntriesAfterIndex(2)
	if err != nil {
		t.Fatal()
	}
	expectedEntries = []LogEntry{
		{1, Command("c3")},
		{4, Command("c4")},
	}
	if !reflect.DeepEqual(actualEntries, expectedEntries) {
		t.Fatal(actualEntries)
	}

	// index more than last log entry
	actualEntries, err = imle.GetEntriesAfterIndex(11)
	if err.Error() != "afterLogIndex=11 is > iole=10" {
		t.Fatal(err)
	}
}

// Helper
func testHelper_GetLogEntryAtIndex(lasm LogAndStateMachine, li LogIndex) LogEntry {
	if li == 0 {
		panic("oops!")
	}
	entries, err := lasm.GetEntriesAfterIndex(li - 1)
	if err != nil {
		panic(err)
	}
	return entries[0]
}
