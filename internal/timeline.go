package internal

import (
	"fmt"
	"github.com/jackc/pgx"
	"github.com/pkg/errors"
	"github.com/wal-g/wal-g/internal/tracelog"
	"strconv"
)

type BytesPerWalSegmentError struct {
	error
}

func NewBytesPerWalSegmentError() BytesPerWalSegmentError {
	return BytesPerWalSegmentError{errors.New("bytes_per_wal_segment of the server does not match expected value")}
}

func (err BytesPerWalSegmentError) Error() string {
	return fmt.Sprintf(tracelog.GetErrorFormatter(), err.error)
}

type IncorrectLogSegNoError struct {
	error
}

func NewIncorrectLogSegNoError(name string) IncorrectLogSegNoError {
	return IncorrectLogSegNoError{errors.Errorf("Incorrect logSegNoLo in WAL file name: %s", name)}
}

func (err IncorrectLogSegNoError) Error() string {
	return fmt.Sprintf(tracelog.GetErrorFormatter(), err.error)
}

func readTimeline(conn *pgx.Conn) (timeline uint32, err error) {
	var bytesPerWalSegment uint32

	// TODO: Check if this logic can be moved to queryRunner or abstracted away somehow
	err = conn.QueryRow("select timeline_id, bytes_per_wal_segment from pg_control_checkpoint(), pg_control_init()").Scan(&timeline, &bytesPerWalSegment)
	if err == nil && uint64(bytesPerWalSegment) != WalSegmentSize {
		return 0, NewBytesPerWalSegmentError()
	}
	return
}

const (
	sizeofInt32bits = sizeofInt32 * 8
)

const (
	// WalSegmentSize is the size of one WAL file
	WalSegmentSize = uint64(16 * 1024 * 1024) // xlog.c line 113ß

	walFileFormat         = "%08X%08X%08X"               // xlog_internal.h line 155
	xLogSegmentsPerXLogId = 0x100000000 / WalSegmentSize // xlog_internal.h line 101
)

func logSegNoFromLsn(lsn uint64) uint64 {
	return (lsn - 1) / WalSegmentSize // xlog_internal.h line 121
}

// getWalFilename formats WAL file name using PostgreSQL connection. Essentially reads timeline of the server.
func getWalFilename(lsn uint64, conn *pgx.Conn) (walFilename string, timeline uint32, err error) {
	timeline, err = readTimeline(conn)
	if err != nil {
		return "", 0, err
	}

	logSegNo := logSegNoFromLsn(lsn)

	return formatWALFileName(timeline, logSegNo), timeline, nil
}

func formatWALFileName(timeline uint32, logSegNo uint64) string {
	return fmt.Sprintf(walFileFormat, timeline, logSegNo/xLogSegmentsPerXLogId, logSegNo%xLogSegmentsPerXLogId)
}

// TODO : unit tests
// ParseWALFilename extracts numeric parts from WAL file name
func ParseWALFilename(name string) (timelineId uint32, logSegNo uint64, err error) {
	if len(name) != 24 {
		err = NewNotWalFilenameError(name)
		return
	}
	timelineId64, err0 := strconv.ParseUint(name[0:8], 0x10, sizeofInt32bits)
	timelineId = uint32(timelineId64)
	if err0 != nil {
		err = err0
		return
	}
	logSegNoHi, err0 := strconv.ParseUint(name[8:16], 0x10, sizeofInt32bits)
	if err0 != nil {
		err = err0
		return
	}
	logSegNoLo, err0 := strconv.ParseUint(name[16:24], 0x10, sizeofInt32bits)
	if err0 != nil {
		err = err0
		return
	}
	if logSegNoLo >= xLogSegmentsPerXLogId {
		err = NewIncorrectLogSegNoError(name)
		return
	}

	logSegNo = logSegNoHi*xLogSegmentsPerXLogId + logSegNoLo
	return
}

func isWalFilename(filename string) bool {
	_, _, err := ParseWALFilename(filename)
	return err == nil
}

// GetNextWalFilename computes name of next WAL segment
func GetNextWalFilename(name string) (string, error) {
	timelineId, logSegNo, err := ParseWALFilename(name)
	if err != nil {
		return "", err
	}
	logSegNo++
	return formatWALFileName(uint32(timelineId), logSegNo), nil
}

func ShouldPrefault(name string) (lsn uint64, shouldPrefault bool, timelineId uint32, err error) {
	timelineId, logSegNo, err := ParseWALFilename(name)
	if err != nil {
		return 0, false, 0, err
	}
	if logSegNo%WalFileInDelta != 0 {
		return 0, false, 0, nil
	}
	logSegNo += WalFileInDelta

	return logSegNo * WalSegmentSize, true, timelineId, nil
}
