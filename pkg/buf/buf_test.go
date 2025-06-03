package buf_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/beeper/argo-go/pkg/buf"
)

const defaultInitialBufferSizeForTest = 32 // Matches 'buf.defaultInitialBufferSize' in the provided code

// Helper to check Buf state
func checkBufState(t *testing.T, b *buf.Buf, testName string,
	expectedPos, expectedLen int64, expectedCapAtLeast int, expectedData []byte) {
	t.Helper()
	if b.Position() != expectedPos {
		t.Errorf("%s: Position() got %d, want %d", testName, b.Position(), expectedPos)
	}
	if int64(b.Len()) != expectedLen {
		t.Errorf("%s: Len() got %d, want %d", testName, b.Len(), expectedLen)
	}
	actualCap := b.Cap()
	if actualCap < expectedCapAtLeast {
		t.Errorf("%s: Cap() got %d, want at least %d", testName, actualCap, expectedCapAtLeast)
	}

	retBytes := b.Bytes()
	if !bytes.Equal(retBytes, expectedData) {
		t.Errorf("%s: Bytes() got %q, want %q", testName, retBytes, expectedData)
	}
	if int64(len(retBytes)) != expectedLen {
		t.Errorf("%s: len(Bytes()) got %d, want %d (should be logical length)", testName, len(retBytes), expectedLen)
	}
}

// TestNewBuf tests the NewBuf constructor.
func TestNewBuf(t *testing.T) {
	tests := []struct {
		name            string
		initialCapacity int
		expectedCap     int
		expectedLen     int64
		expectedPos     int64
		expectedData    []byte
	}{
		{"PositiveCapacity", 64, 64, 0, 0, []byte{}},
		{"ZeroCapacity", 0, 0, 0, 0, []byte{}}, // make([]byte, 0, 0) is valid
		{"NegativeCapacity", -1, defaultInitialBufferSizeForTest, 0, 0, []byte{}},
		{"SmallPositiveCapacity", 1, 1, 0, 0, []byte{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := buf.NewBuf(tt.initialCapacity)
			if b.Cap() != tt.expectedCap {
				t.Errorf("Cap() got %d, want %d", b.Cap(), tt.expectedCap)
			}
			if int64(b.Len()) != tt.expectedLen {
				t.Errorf("Len() got %d, want %d", b.Len(), tt.expectedLen)
			}
			if b.Position() != tt.expectedPos {
				t.Errorf("Position() got %d, want %d", b.Position(), tt.expectedPos)
			}
			if !bytes.Equal(b.Bytes(), tt.expectedData) {
				t.Errorf("Bytes() got %q, want %q", b.Bytes(), tt.expectedData)
			}
		})
	}
}

// TestBuf_Reset verifies the Reset method's behavior on both empty and non-empty buffers,
// ensuring the buffer is cleared, position and length are reset to zero, and capacity is retained.
// It also checks that the buffer can be reused after reset.
func TestBuf_Reset(t *testing.T) {
	t.Run("ResetEmptyBuffer", func(t *testing.T) {
		b := buf.NewBuf(10)
		originalCap := b.Cap()
		b.Reset()
		checkBufState(t, b, "ResetEmptyBuffer", 0, 0, originalCap, []byte{})
		if b.Cap() != originalCap { // Capacity should be retained
			t.Errorf("Cap changed after Reset: got %d, want %d", b.Cap(), originalCap)
		}
	})

	t.Run("ResetNonEmptyBuffer", func(t *testing.T) {
		b := buf.NewBuf(10)
		initialData := []byte("hello")
		_, _ = b.Write(initialData) // pos becomes 5, len becomes 5
		b.SetPosition(2)
		originalCap := b.Cap()

		b.Reset()
		checkBufState(t, b, "ResetNonEmptyBuffer", 0, 0, originalCap, []byte{})
		if b.Cap() != originalCap {
			t.Errorf("Cap changed after Reset: got %d, want %d", b.Cap(), originalCap)
		}

		// Ensure it can be reused
		nextData := []byte("world")
		n, err := b.Write(nextData)
		if err != nil {
			t.Fatalf("Write after Reset failed: %v", err)
		}
		if n != len(nextData) {
			t.Errorf("Write after Reset: wrong byte count, got %d, want %d", n, len(nextData))
		}
		checkBufState(t, b, "WriteAfterReset", int64(len(nextData)), int64(len(nextData)), originalCap, nextData)
	})
}

// TestBuf_PositionManagement tests the SetPosition and IncrementPosition methods
// to ensure they correctly update the buffer's internal position tracker.
func TestBuf_PositionManagement(t *testing.T) {
	b := buf.NewBuf(10)
	if b.Position() != 0 {
		t.Errorf("Initial Position() got %d, want 0", b.Position())
	}

	b.SetPosition(5)
	if b.Position() != 5 {
		t.Errorf("SetPosition(5): Position() got %d, want 5", b.Position())
	}

	b.IncrementPosition(3)
	if b.Position() != 8 {
		t.Errorf("IncrementPosition(3): Position() got %d, want 8", b.Position())
	}

	b.IncrementPosition(-2)
	if b.Position() != 6 {
		t.Errorf("IncrementPosition(-2): Position() got %d, want 6", b.Position())
	}

	b.SetPosition(-10) // Allowed by SetPosition, bounds checked by Read/Write
	if b.Position() != -10 {
		t.Errorf("SetPosition(-10): Position() got %d, want -10", b.Position())
	}
}

// TestBuf_Write tests the Write method under various scenarios,
// including writing to an empty buffer, appending, overwriting, writing with gaps (causing zero-filling),
// causing buffer growth, writing empty slices, and attempting to write at invalid positions.
func TestBuf_Write(t *testing.T) {
	tests := []struct {
		name                string
		bufInitialCap       int    // Capacity for NewBuf
		initialData         []byte // data to write first to setup buffer
		writeData           []byte
		writePos            int64 // position to set before writing writeData
		expectedErrStr      string
		expectedFinalData   []byte
		expectedFinalPos    int64
		expectedFinalLen    int64
		expectedFinalMinCap int // minimum capacity to check after write
	}{
		{
			name:                "WriteToEmptyBuffer",
			bufInitialCap:       0,
			writeData:           []byte("hello"),
			writePos:            0,
			expectedFinalData:   []byte("hello"),
			expectedFinalPos:    5,
			expectedFinalLen:    5,
			expectedFinalMinCap: 5, // Actual growth might be to 16
		},
		{
			name:                "AppendToBuffer",
			bufInitialCap:       10,
			initialData:         []byte("hello"),  // len 5, pos 5
			writeData:           []byte(" world"), // len 6
			writePos:            5,                // append. endPos = 5+6 = 11. NewLen = 11
			expectedFinalData:   []byte("hello world"),
			expectedFinalPos:    11,
			expectedFinalLen:    11,
			expectedFinalMinCap: 11, // Needs growth from 10 to 11 (likely 20)
		},
		{
			name:                "OverwriteStartOfBuffer",
			bufInitialCap:       20,
			initialData:         []byte("hello world"), // len 11, pos 11
			writeData:           []byte("HELLO"),       // len 5
			writePos:            0,                     // overwrite from start. endPos = 0+5=5. NewLen = max(11, 5) = 11.
			expectedFinalData:   []byte("HELLO world"),
			expectedFinalPos:    5,
			expectedFinalLen:    11,
			expectedFinalMinCap: 20, // Cap should not shrink
		},
		{
			name:                "WriteBeyondCurrentLengthWithGap",
			bufInitialCap:       10,
			initialData:         []byte("abc"), // len 3, pos 3
			writeData:           []byte("fgh"), // len 3
			writePos:            5,             // write at pos 5. endPos = 5+3=8. NewLen = 8.
			expectedFinalData:   []byte{'a', 'b', 'c', 0, 0, 'f', 'g', 'h'},
			expectedFinalPos:    8,
			expectedFinalLen:    8,
			expectedFinalMinCap: 8, // Cap 10 is sufficient
		},
		{
			name:                "WriteCausesGrowthFromZeroCap",
			bufInitialCap:       0,
			writeData:           []byte("long string to ensure growth"),
			writePos:            0,
			expectedFinalData:   []byte("long string to ensure growth"),
			expectedFinalPos:    int64(len("long string to ensure growth")),
			expectedFinalLen:    int64(len("long string to ensure growth")),
			expectedFinalMinCap: len("long string to ensure growth"),
		},
		{
			name:                "WriteEmptySlice",
			bufInitialCap:       10,
			initialData:         []byte("test"), // len 4, pos 4
			writeData:           []byte{},
			writePos:            2, // Set pos to 2
			expectedFinalData:   []byte("test"),
			expectedFinalPos:    2, // position remains where it was set for a zero-byte write
			expectedFinalLen:    4,
			expectedFinalMinCap: 10,
		},
		{
			name:                "WriteAtNegativePosition",
			bufInitialCap:       0, // Start with 0 cap
			writeData:           []byte("test"),
			writePos:            -1,
			expectedErrStr:      "argo.Buf.Write: negative position",
			expectedFinalData:   []byte{}, // No initial data, write fails
			expectedFinalPos:    -1,       // Position was set to -1
			expectedFinalLen:    0,
			expectedFinalMinCap: 0, // Cap remains 0
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := buf.NewBuf(tt.bufInitialCap)

			if tt.initialData != nil {
				// Write initial data and ignore its effect on pos for test setup simplicity,
				// as tt.writePos will explicitly set the position for the Write under test.
				_, err := b.Write(tt.initialData)
				if err != nil {
					t.Fatalf("Setup: failed to write initial data: %v", err)
				}
			}

			capBeforeWrite := b.Cap() // Capture cap after initialData, before targeted Write
			lenBeforeWrite := b.Len()
			bytesBeforeWrite := make([]byte, lenBeforeWrite)
			if lenBeforeWrite > 0 {
				copy(bytesBeforeWrite, b.Bytes())
			}

			b.SetPosition(tt.writePos) // Set position for the Write operation being tested

			n, err := b.Write(tt.writeData)

			if tt.expectedErrStr != "" {
				if err == nil {
					t.Errorf("Write() error = nil, want %q", tt.expectedErrStr)
				} else if err.Error() != tt.expectedErrStr {
					t.Errorf("Write() error = %q, want %q", err.Error(), tt.expectedErrStr)
				}
				// Check state after failed write
				if b.Position() != tt.expectedFinalPos {
					t.Errorf("Position() after failed Write: got %d, want %d", b.Position(), tt.expectedFinalPos)
				}
				// For failed writes, length and data should be what they were before the SetPosition + Write call,
				// unless the test case specifically expects them to change (e.g. if SetPosition was part of the fail logic).
				// Here, we expect them to match the state captured *after* initialData write but *before* the failing Write attempt (or its setup SetPosition)
				// However, tt.expectedFinalLen/Data are defined for the error state.
				if b.Len() != int(tt.expectedFinalLen) {
					t.Errorf("Len() after failed Write: got %d, want %d", b.Len(), tt.expectedFinalLen)
				}
				if !bytes.Equal(b.Bytes(), tt.expectedFinalData) {
					t.Errorf("Bytes() after failed Write: got %q, want %q", b.Bytes(), tt.expectedFinalData)
				}
				// Capacity should not change if ensureCapacity is not called before the error or if the error prevents modification.
				// The Write function checks pos < 0 first.
				expectedCapAfterFailedWrite := capBeforeWrite
				if tt.name == "WriteAtNegativePosition" && tt.bufInitialCap == 0 && tt.initialData == nil {
					// Special case if buf was NewBuf(0) and no initial data, cap is 0.
					expectedCapAfterFailedWrite = 0
				} else if tt.name == "WriteAtNegativePosition" {
					expectedCapAfterFailedWrite = tt.bufInitialCap // Cap remains what NewBuf set it to
				}

				if b.Cap() != expectedCapAfterFailedWrite {
					t.Errorf("Cap() after failed Write: got %d, want %d", b.Cap(), expectedCapAfterFailedWrite)
				}
				return
			}

			if err != nil {
				t.Fatalf("Write() unexpected error: %v", err)
			}
			if n != len(tt.writeData) {
				t.Errorf("Write() n = %d, want %d", n, len(tt.writeData))
			}

			checkBufState(t, b, tt.name, tt.expectedFinalPos, tt.expectedFinalLen, tt.expectedFinalMinCap, tt.expectedFinalData)
		})
	}
}

// TestBuf_Read tests the Read method for various scenarios, including reading from an empty buffer,
// reading into zero-length slices, full and partial reads, reading from various positions (middle, end, beyond),
// and reading from an invalid negative position.
func TestBuf_Read(t *testing.T) {
	baseData := []byte("0123456789abcdef") // 16 bytes
	tests := []struct {
		name             string
		initialData      []byte
		initialPos       int64
		readBufSize      int
		expectedN        int
		expectedReadData []byte // content of readBuf after read (only first N bytes matter)
		expectedErr      error
		expectedFinalPos int64
	}{
		{"ReadFromEmptyBuffer", []byte{}, 0, 10, 0, make([]byte, 10), io.EOF, 0},
		// Standard behavior: Read into empty slice p should return n=0, err=nil.
		{"ReadIntoZeroLenSliceWhenDataAvailable", baseData, 0, 0, 0, []byte{}, nil, 0},
		{"ReadAllContent", baseData, 0, len(baseData), len(baseData), baseData, nil, int64(len(baseData))},
		{"ReadPartialContent", baseData, 0, 5, 5, []byte("01234"), nil, 5},
		{"ReadFromMiddle", baseData, 5, 5, 5, []byte("56789"), nil, 10},
		{"ReadPastEnd", baseData, 10, 10, 6, []byte("abcdef"), nil, 16},
		{"ReadAtEnd", baseData, int64(len(baseData)), 5, 0, make([]byte, 5), io.EOF, int64(len(baseData))},
		{"ReadBeyondEnd", baseData, int64(len(baseData)) + 5, 5, 0, make([]byte, 5), io.EOF, int64(len(baseData)) + 5},
		{"ReadNegativePosition", baseData, -2, 5, 0, make([]byte, 5), io.EOF, -2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := buf.NewBuf(len(tt.initialData))
			if len(tt.initialData) > 0 {
				_, _ = b.Write(tt.initialData)
			}
			b.SetPosition(tt.initialPos)

			readBuf := make([]byte, tt.readBufSize)
			n, err := b.Read(readBuf)

			if !errors.Is(err, tt.expectedErr) {
				t.Errorf("Read() error got %v, want %v", err, tt.expectedErr)
			}
			if n != tt.expectedN {
				t.Errorf("Read() n got %d, want %d", n, tt.expectedN)
			}
			if !bytes.Equal(readBuf[:n], tt.expectedReadData[:n]) {
				t.Errorf("Read() data got %q, want %q", readBuf[:n], tt.expectedReadData[:n])
			}
			if b.Position() != tt.expectedFinalPos {
				t.Errorf("Read() final position got %d, want %d", b.Position(), tt.expectedFinalPos)
			}
		})
	}
}

// TestBuf_ReadByte tests the ReadByte method, covering sequential reads through buffer content,
// reading at and beyond the end of the buffer (EOF), reading from an invalid negative position (EOF),
// and reading from an empty buffer (EOF).
func TestBuf_ReadByte(t *testing.T) {
	data := []byte("abc")
	b := buf.NewBuf(0)
	_, _ = b.Write(data)

	b.SetPosition(0)
	for i := 0; i < len(data); i++ {
		bt, err := b.ReadByte()
		if err != nil {
			t.Fatalf("ReadByte() at pos %d: unexpected error %v", i, err)
		}
		if bt != data[i] {
			t.Errorf("ReadByte() at pos %d: got %c, want %c", i, bt, data[i])
		}
		if b.Position() != int64(i+1) {
			t.Errorf("Position after ReadByte() at pos %d: got %d, want %d", i, b.Position(), i+1)
		}
	}

	_, err := b.ReadByte()
	if !errors.Is(err, io.EOF) {
		t.Errorf("ReadByte() at end: error got %v, want io.EOF", err)
	}
	b.SetPosition(int64(len(data) + 5))
	_, err = b.ReadByte()
	if !errors.Is(err, io.EOF) {
		t.Errorf("ReadByte() pos > length: error got %v, want io.EOF", err)
	}
	b.SetPosition(-1)
	_, err = b.ReadByte()
	if !errors.Is(err, io.EOF) {
		t.Errorf("ReadByte() pos < 0: error got %v, want io.EOF", err)
	}
	emptyBuf := buf.NewBuf(0)
	_, err = emptyBuf.ReadByte()
	if !errors.Is(err, io.EOF) {
		t.Errorf("ReadByte() from empty buffer: error got %v, want io.EOF", err)
	}
}

// TestBuf_WriteByte tests the WriteByte method across different scenarios:
// writing to an empty buffer, appending, overwriting existing bytes, causing buffer growth,
// writing with a gap (expecting zero-filling), and attempting to write at an invalid negative position.
func TestBuf_WriteByte(t *testing.T) {
	tests := []struct {
		name                string
		bufInitialCap       int
		initialData         []byte
		byteToWrite         byte
		writePos            int64
		expectedErrStr      string
		expectedFinalData   []byte
		expectedFinalPos    int64
		expectedFinalLen    int64
		expectedFinalMinCap int
	}{
		{"WriteByteToEmpty", 0, nil, 'a', 0, "", []byte{'a'}, 1, 1, 1},
		{"AppendByte", 5, []byte("hi"), '!', 2, "", []byte("hi!"), 3, 3, 3},
		{"OverwriteByte", 5, []byte("cat"), 'r', 1, "", []byte("crt"), 2, 3, 3},
		{"WriteByteCausingGrowth", 0, nil, 'g', 0, "", []byte{'g'}, 1, 1, 1},
		{
			name:                "WriteByteWithGap",
			bufInitialCap:       5,
			initialData:         []byte("xy"), // len 2, pos 2
			byteToWrite:         'z',
			writePos:            3, // current length 2, write at pos 3. endPos=4. NewLen=4
			expectedErrStr:      "",
			expectedFinalData:   []byte{'x', 'y', 0, 'z'},
			expectedFinalPos:    4,
			expectedFinalLen:    4,
			expectedFinalMinCap: 4,
		},
		{
			name:                "WriteByteNegativePosition",
			bufInitialCap:       5,
			initialData:         nil,
			byteToWrite:         'e',
			writePos:            -1,
			expectedErrStr:      "argo.Buf.WriteByte: negative position",
			expectedFinalData:   []byte{},
			expectedFinalPos:    -1,
			expectedFinalLen:    0,
			expectedFinalMinCap: 5, // Cap should remain as set by NewBuf
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := buf.NewBuf(tt.bufInitialCap)
			if tt.initialData != nil {
				_, _ = b.Write(tt.initialData)
			}
			capBeforeWriteByte := b.Cap()
			b.SetPosition(tt.writePos)

			err := b.WriteByte(tt.byteToWrite)

			if tt.expectedErrStr != "" {
				if err == nil {
					t.Errorf("WriteByte() error = nil, want %q", tt.expectedErrStr)
				} else if err.Error() != tt.expectedErrStr {
					t.Errorf("WriteByte() error = %q, want %q", err.Error(), tt.expectedErrStr)
				}
				if b.Position() != tt.expectedFinalPos {
					t.Errorf("Position() after failed WriteByte: got %d, want %d", b.Position(), tt.expectedFinalPos)
				}
				if b.Len() != int(tt.expectedFinalLen) {
					t.Errorf("Len() after failed WriteByte: got %d, want %d", b.Len(), tt.expectedFinalLen)
				}
				if !bytes.Equal(b.Bytes(), tt.expectedFinalData) {
					t.Errorf("Bytes() after failed WriteByte: got %q, want %q", b.Bytes(), tt.expectedFinalData)
				}
				if b.Cap() != capBeforeWriteByte {
					t.Errorf("Cap() after failed WriteByte: got %d, want %d (cap before write attempt)", b.Cap(), capBeforeWriteByte)
				}
				return
			}

			if err != nil {
				t.Fatalf("WriteByte() unexpected error: %v", err)
			}
			checkBufState(t, b, tt.name, tt.expectedFinalPos, tt.expectedFinalLen, tt.expectedFinalMinCap, tt.expectedFinalData)
		})
	}
}

// TestBuf_Get tests the Get method to retrieve bytes by absolute position without affecting the buffer's current read/write position.
// It checks getting valid bytes and attempting to get bytes from invalid positions (out of bounds, negative).
func TestBuf_Get(t *testing.T) {
	data := []byte("01234")
	b := buf.NewBuf(0)
	_, _ = b.Write(data)
	b.SetPosition(1) // Ensure Get doesn't change current position

	for i := 0; i < len(data); i++ {
		bt, err := b.Get(int64(i))
		if err != nil {
			t.Fatalf("Get(%d): unexpected error %v", i, err)
		}
		if bt != data[i] {
			t.Errorf("Get(%d): got %c, want %c", i, bt, data[i])
		}
		if b.Position() != 1 {
			t.Errorf("Get(%d): Position changed: got %d, want 1", i, b.Position())
		}
	}

	invalidPositions := []int64{-1, int64(len(data)), int64(len(data)) + 1}
	for _, pos := range invalidPositions {
		_, err := b.Get(pos)
		if !errors.Is(err, io.EOF) {
			t.Errorf("Get(%d): error got %v, want io.EOF", pos, err)
		}
		if b.Position() != 1 {
			t.Errorf("Get(%d): Position changed: got %d, want 1", pos, b.Position())
		}
	}

	emptyBuf := buf.NewBuf(0)
	_, err := emptyBuf.Get(0)
	if !errors.Is(err, io.EOF) {
		t.Errorf("Get(0) from empty buffer: error got %v, want io.EOF", err)
	}
}

// TestBuf_Bytes tests the Bytes method, ensuring it returns the correct slice of the buffer's content
// for both empty and non-empty buffers. It also verifies that the returned slice's length matches
// the buffer's logical length and tests the aliasing behavior (modifications to the returned slice
// may affect the buffer's internal data if no reallocation has occurred).
func TestBuf_Bytes(t *testing.T) {
	t.Run("EmptyBuffer", func(t *testing.T) {
		b := buf.NewBuf(10)
		if !bytes.Equal(b.Bytes(), []byte{}) {
			t.Errorf("Bytes() on empty buffer: got %q, want []byte{}", b.Bytes())
		}
	})

	t.Run("NonEmptyBuffer", func(t *testing.T) {
		data := []byte("hello")
		b := buf.NewBuf(10)
		_, _ = b.Write(data) // Len 5, Cap 10

		retBytes := b.Bytes()
		if !bytes.Equal(retBytes, data) {
			t.Errorf("Bytes() on non-empty buffer: got %q, want %q", retBytes, data)
		}
		if len(retBytes) != len(data) {
			t.Errorf("len(Bytes()) got %d, want %d (logical length)", len(retBytes), len(data))
		}
		if cap(retBytes) < len(data) { // cap(b.Bytes()) should be at least b.Len()
			t.Errorf("cap(Bytes()) got %d, want at least %d", cap(retBytes), len(data))
		}
		// Test aliasing: modification to returned slice may affect buffer if no reallocation.
		if len(retBytes) > 0 {
			originalFirstByte := retBytes[0]
			retBytes[0] = 'X' // Modify slice returned by Bytes()
			// Buf.Bytes() returns b.data[:b.length]. Modification of this slice modifies b.data.
			if b.Bytes()[0] != 'X' {
				t.Error("Modification of Bytes() slice did not affect buffer's internal data (expected aliasing)")
			}
			retBytes[0] = originalFirstByte // Restore
		}
	})
}

// TestBuf_Len tests the Len method, verifying it accurately reports the buffer's logical length
// initially, after writes, and after a reset.
func TestBuf_Len(t *testing.T) {
	b := buf.NewBuf(10)
	if b.Len() != 0 {
		t.Errorf("Len() initial: got %d, want 0", b.Len())
	}
	_, _ = b.Write([]byte("test"))
	if b.Len() != 4 {
		t.Errorf("Len() after Write: got %d, want 4", b.Len())
	}
	_, _ = b.Write([]byte(" more"))
	if b.Len() != 9 { // "test more"
		t.Errorf("Len() after second Write: got %d, want 9", b.Len())
	}
	b.Reset()
	if b.Len() != 0 {
		t.Errorf("Len() after Reset: got %d, want 0", b.Len())
	}
}

// TestBuf_Cap tests the Cap method, checking the buffer's capacity after initialization
// (with positive, zero, and negative initial capacities), after writes that cause growth
// (including growth from zero capacity), and after a reset (capacity should be retained).
func TestBuf_Cap(t *testing.T) {
	t.Run("InitialCap", func(t *testing.T) {
		b := buf.NewBuf(10)
		if b.Cap() != 10 {
			t.Errorf("Cap() initial: got %d, want 10", b.Cap())
		}
		bNeg := buf.NewBuf(-1)
		if bNeg.Cap() != defaultInitialBufferSizeForTest {
			t.Errorf("Cap() initial for negative: got %d, want %d", bNeg.Cap(), defaultInitialBufferSizeForTest)
		}
	})

	t.Run("CapAfterGrowth", func(t *testing.T) {
		b := buf.NewBuf(5)               // Cap 5
		_, _ = b.Write(make([]byte, 10)) // Write 10 bytes. Needs growth. Expected cap 10.
		if b.Cap() != 10 {
			t.Errorf("Cap() after 1st growth (5 -> 10): got %d, want 10", b.Cap())
		}
		_, _ = b.Write(make([]byte, 20)) // Write 20 more. Total 30. Current len 10, pos 10. EndPos 30. Expected cap 40.
		if b.Cap() != 40 {
			t.Errorf("Cap() after 2nd growth (10 -> 30, cap to 40): got %d, want 40", b.Cap())
		}
	})

	t.Run("CapAfterGrowthFromZero", func(t *testing.T) {
		b := buf.NewBuf(0)
		_, _ = b.Write(make([]byte, 5)) // minCap 5. Grows to 16.
		if b.Cap() != 16 {
			t.Errorf("Cap() after growth from 0 (minCap 5): got %d, want 16", b.Cap())
		}

		b2 := buf.NewBuf(0)
		_, _ = b2.Write(make([]byte, 20)) // minCap 20. Grows to 20.
		if b2.Cap() != 20 {
			t.Errorf("Cap() after growth from 0 (minCap 20): got %d, want 20", b2.Cap())
		}
	})

	t.Run("CapAfterReset", func(t *testing.T) {
		b := buf.NewBuf(5)
		_, _ = b.Write(make([]byte, 10)) // Cap is now 10
		capBeforeReset := b.Cap()
		b.Reset()
		if b.Cap() != capBeforeReset {
			t.Errorf("Cap() after Reset: got %d, want %d (should be retained)", b.Cap(), capBeforeReset)
		}
	})
}

// TestBuf_Peek tests the Peek method for various scenarios, including peeking different amounts of data
// from various positions (start, middle), handling cases where requested bytes exceed available data,
// peeking at/beyond the end, from negative positions, and with zero or negative counts for n.
// It ensures Peek does not advance the buffer's position.
func TestBuf_Peek(t *testing.T) {
	data := []byte("0123456789")
	b := buf.NewBuf(0)
	_, _ = b.Write(data)

	tests := []struct {
		name             string
		pos              int64
		peekN            int
		expectedPeekData []byte
		expectedErrStr   string
		expectedFinalPos int64
	}{
		{"PeekLessThanAvailable", 0, 3, []byte("012"), "", 0},
		{"PeekAllAvailableFromStart", 0, 10, []byte("0123456789"), "", 0},
		{"PeekMoreThanAvailableFromStart", 0, 15, []byte("0123456789"), "", 0},
		{"PeekFromMiddle", 5, 3, []byte("567"), "", 5},
		{"PeekMoreThanAvailableFromMiddle", 5, 10, []byte("56789"), "", 5},
		{"PeekAtEnd", 10, 5, nil, "EOF", 10},
		{"PeekBeyondEnd", 11, 5, nil, "EOF", 11},
		{"PeekNegativePosition", -1, 5, nil, "EOF", -1},
		{"PeekNIsZero", 2, 0, []byte{}, "", 2},
		{"PeekNIsNegative", 2, -1, nil, "argo.Buf.Peek: count cannot be negative", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b.SetPosition(tt.pos)
			peekedBytes, err := b.Peek(tt.peekN)

			if tt.expectedErrStr != "" {
				if err == nil {
					t.Errorf("Peek() error = nil, want %q", tt.expectedErrStr)
				} else if err.Error() != tt.expectedErrStr {
					if tt.expectedErrStr == "EOF" && errors.Is(err, io.EOF) {
						// This is fine
					} else {
						t.Errorf("Peek() error = %q, want %q", err.Error(), tt.expectedErrStr)
					}
				}
			} else if err != nil {
				t.Errorf("Peek() unexpected error: %v", err)
			}

			if !bytes.Equal(peekedBytes, tt.expectedPeekData) {
				t.Errorf("Peek() data got %q, want %q", peekedBytes, tt.expectedPeekData)
			}
			if b.Position() != tt.expectedFinalPos {
				t.Errorf("Peek() final position got %d, want %d", b.Position(), tt.expectedFinalPos)
			}
		})
	}

	emptyBuf := buf.NewBuf(0)
	_, err := emptyBuf.Peek(1)
	if !errors.Is(err, io.EOF) {
		t.Errorf("Peek(1) from empty buffer: error got %v, want io.EOF", err)
	}
}

// TestBuf_WriteBuf tests the WriteBuf method, which writes the content of one Buf into another.
// Scenarios include writing an empty buffer, writing a non-empty buffer to an empty one, appending,
// and attempting to write to a target buffer with an invalid (negative) position.
func TestBuf_WriteBuf(t *testing.T) {
	t.Run("WriteEmptyBufToEmptyBuf", func(t *testing.T) {
		b1 := buf.NewBuf(0)
		b2 := buf.NewBuf(0)
		n, err := b1.WriteBuf(b2)
		if err != nil {
			t.Fatalf("WriteBuf failed: %v", err)
		}
		if n != 0 {
			t.Errorf("WriteBuf n got %d, want 0", n)
		}
		checkBufState(t, b1, "b1", 0, 0, 0, []byte{})
	})

	t.Run("WriteNonEmptyBufToEmptyBuf", func(t *testing.T) {
		b1 := buf.NewBuf(0)
		b2 := buf.NewBuf(0)
		data := []byte("hello")
		_, _ = b2.Write(data)
		// b2.pos is now len(data). WriteBuf uses b2.Bytes(), which is independent of b2.pos.

		n, err := b1.WriteBuf(b2)
		if err != nil {
			t.Fatalf("WriteBuf failed: %v", err)
		}
		if n != len(data) {
			t.Errorf("WriteBuf n got %d, want %d", n, len(data))
		}
		checkBufState(t, b1, "b1", int64(len(data)), int64(len(data)), len(data), data)
	})

	t.Run("WriteBufAppending", func(t *testing.T) {
		b1 := buf.NewBuf(0)
		data1 := []byte("part1 ")
		_, _ = b1.Write(data1) // b1.pos is now len(data1)

		b2 := buf.NewBuf(0)
		data2 := []byte("part2")
		_, _ = b2.Write(data2)

		n, err := b1.WriteBuf(b2)
		if err != nil {
			t.Fatalf("WriteBuf failed: %v", err)
		}
		expectedFullData := append(data1, data2...)
		if n != len(data2) { // WriteBuf writes content of b2.Bytes(), so n is len(data2)
			t.Errorf("WriteBuf n got %d, want %d", n, len(data2))
		}
		checkBufState(t, b1, "b1", int64(len(expectedFullData)), int64(len(expectedFullData)), len(expectedFullData), expectedFullData)
	})

	t.Run("WriteBufWithNegativePositionInTarget", func(t *testing.T) {
		b1 := buf.NewBuf(10) // Initial cap 10
		b2 := buf.NewBuf(0)
		_, _ = b2.Write([]byte("test"))

		b1.SetPosition(-1) // Set target buffer's position to negative

		n, err := b1.WriteBuf(b2)

		if err == nil {
			t.Errorf("WriteBuf expected an error for negative position, got nil")
		} else if err.Error() != "argo.Buf.Write: negative position" {
			t.Errorf("WriteBuf error got %q, want %q", err.Error(), "argo.Buf.Write: negative position")
		}
		if n != 0 {
			t.Errorf("WriteBuf n got %d, want 0 on error", n)
		}
		// State of b1: pos should be -1, len 0, data empty, cap 10 (initial)
		checkBufState(t, b1, "b1 after failed WriteBuf", -1, 0, 10, []byte{})
		if b1.Cap() != 10 { // Exact cap check
			t.Errorf("b1 Cap after failed WriteBuf got %d, want 10", b1.Cap())
		}
	})
}

// --- BufReadonly Tests ---

// TestNewBufReadonly tests the NewBufReadonly constructor to ensure it correctly initializes
// a BufReadonly instance with nil, empty, or non-empty byte slices, verifying initial
// length, position, and the content returned by Bytes().
func TestNewBufReadonly(t *testing.T) {
	tests := []struct {
		name          string
		inputData     []byte
		expectedLen   int
		expectedPos   int64
		expectedBytes []byte // What Bytes() should return
	}{
		{"NilData", nil, 0, 0, nil}, // Bytes() returns br.bytes which can be nil
		{"EmptyData", []byte{}, 0, 0, []byte{}},
		{"NonEmptyData", []byte("test"), 4, 0, []byte("test")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			br := buf.NewBufReadonly(tt.inputData)
			if br.Len() != tt.expectedLen {
				t.Errorf("Len() got %d, want %d", br.Len(), tt.expectedLen)
			}
			if br.Position() != tt.expectedPos {
				t.Errorf("Position() got %d, want %d", br.Position(), tt.expectedPos)
			}
			if !bytes.Equal(br.Bytes(), tt.expectedBytes) {
				t.Errorf("Bytes() got %q, want %q", br.Bytes(), tt.expectedBytes)
			}
		})
	}
}

// TestBufReadonly_PositionManagement tests the SetPosition and IncrementPosition methods
// for BufReadonly to ensure they correctly update its internal position tracker.
func TestBufReadonly_PositionManagement(t *testing.T) {
	br := buf.NewBufReadonly([]byte("0123456789"))
	if br.Position() != 0 {
		t.Errorf("Initial Position() got %d, want 0", br.Position())
	}
	br.SetPosition(5)
	if br.Position() != 5 {
		t.Errorf("SetPosition(5): Position() got %d, want 5", br.Position())
	}
	br.IncrementPosition(3)
	if br.Position() != 8 {
		t.Errorf("IncrementPosition(3): Position() got %d, want 8", br.Position())
	}
	br.IncrementPosition(-2)
	if br.Position() != 6 {
		t.Errorf("IncrementPosition(-2): Position() got %d, want 6", br.Position())
	}
	br.SetPosition(-10)
	if br.Position() != -10 {
		t.Errorf("SetPosition(-10): Position() got %d, want -10", br.Position())
	}
}

// TestBufReadonly_Read tests the Read method for BufReadonly under various conditions,
// including reading from nil or empty underlying data, reading into zero-length slices,
// full and partial reads, and reading from various positions (middle, end, beyond, negative).
func TestBufReadonly_Read(t *testing.T) {
	baseData := []byte("0123456789abcdef") // 16 bytes
	tests := []struct {
		name             string
		initialData      []byte
		initialPos       int64
		readBufSize      int
		expectedN        int
		expectedReadData []byte // content of readBuf after read (only first N bytes matter)
		expectedErr      error
		expectedFinalPos int64
	}{
		{"ReadFromNilUnderlying", nil, 0, 10, 0, make([]byte, 10), io.EOF, 0},
		{"ReadFromEmptyUnderlying", []byte{}, 0, 10, 0, make([]byte, 10), io.EOF, 0},
		{"ReadIntoZeroLenSlice", baseData, 0, 0, 0, []byte{}, nil, 0}, // BufReadonly.Read is standard here
		{"ReadAllContent", baseData, 0, len(baseData), len(baseData), baseData, nil, int64(len(baseData))},
		{"ReadPartialContent", baseData, 0, 5, 5, []byte("01234"), nil, 5},
		{"ReadFromMiddle", baseData, 5, 5, 5, []byte("56789"), nil, 10},
		{"ReadPastEnd", baseData, 10, 10, 6, []byte("abcdef"), nil, 16},
		{"ReadAtEnd", baseData, int64(len(baseData)), 5, 0, make([]byte, 5), io.EOF, int64(len(baseData))},
		{"ReadBeyondEnd", baseData, int64(len(baseData)) + 5, 5, 0, make([]byte, 5), io.EOF, int64(len(baseData)) + 5},
		{"ReadNegativePosition", baseData, -2, 5, 0, make([]byte, 5), io.EOF, -2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			br := buf.NewBufReadonly(tt.initialData)
			br.SetPosition(tt.initialPos)

			readBuf := make([]byte, tt.readBufSize)
			n, err := br.Read(readBuf)

			if !errors.Is(err, tt.expectedErr) {
				t.Errorf("Read() error got %v, want %v", err, tt.expectedErr)
			}
			if n != tt.expectedN {
				t.Errorf("Read() n got %d, want %d", n, tt.expectedN)
			}
			if !bytes.Equal(readBuf[:n], tt.expectedReadData[:n]) {
				t.Errorf("Read() data got %q, want %q", readBuf[:n], tt.expectedReadData[:n])
			}
			if br.Position() != tt.expectedFinalPos {
				t.Errorf("Read() final position got %d, want %d", br.Position(), tt.expectedFinalPos)
			}
		})
	}
}

// TestBufReadonly_ReadByte tests the ReadByte method for BufReadonly, covering sequential reads,
// reading at/beyond the end (EOF), from negative positions (EOF), and from an empty/nil BufReadonly (EOF).
func TestBufReadonly_ReadByte(t *testing.T) {
	data := []byte("abc")
	br := buf.NewBufReadonly(data)

	br.SetPosition(0)
	for i := 0; i < len(data); i++ {
		bt, err := br.ReadByte()
		if err != nil {
			t.Fatalf("ReadByte() at pos %d: unexpected error %v", i, err)
		}
		if bt != data[i] {
			t.Errorf("ReadByte() at pos %d: got %c, want %c", i, bt, data[i])
		}
		if br.Position() != int64(i+1) {
			t.Errorf("Position after ReadByte() at pos %d: got %d, want %d", i, br.Position(), i+1)
		}
	}

	_, err := br.ReadByte()
	if !errors.Is(err, io.EOF) {
		t.Errorf("ReadByte() at end: error got %v, want io.EOF", err)
	}
	br.SetPosition(int64(len(data) + 5))
	_, err = br.ReadByte()
	if !errors.Is(err, io.EOF) {
		t.Errorf("ReadByte() pos > length: error got %v, want io.EOF", err)
	}
	br.SetPosition(-1)
	_, err = br.ReadByte()
	if !errors.Is(err, io.EOF) {
		t.Errorf("ReadByte() pos < 0: error got %v, want io.EOF", err)
	}
	emptyBr := buf.NewBufReadonly(nil)
	_, err = emptyBr.ReadByte()
	if !errors.Is(err, io.EOF) {
		t.Errorf("ReadByte() from empty buffer: error got %v, want io.EOF", err)
	}
}

// TestBufReadonly_Get tests the Get method for BufReadonly to retrieve bytes by absolute position
// without affecting the current read position. It checks valid gets and attempts from invalid positions.
func TestBufReadonly_Get(t *testing.T) {
	data := []byte("01234")
	br := buf.NewBufReadonly(data)
	br.SetPosition(1)

	for i := 0; i < len(data); i++ {
		bt, err := br.Get(int64(i))
		if err != nil {
			t.Fatalf("Get(%d): unexpected error %v", i, err)
		}
		if bt != data[i] {
			t.Errorf("Get(%d): got %c, want %c", i, bt, data[i])
		}
		if br.Position() != 1 {
			t.Errorf("Get(%d): Position changed: got %d, want 1", i, br.Position())
		}
	}

	invalidPositions := []int64{-1, int64(len(data)), int64(len(data)) + 1}
	for _, pos := range invalidPositions {
		_, err := br.Get(pos)
		if !errors.Is(err, io.EOF) {
			t.Errorf("Get(%d): error got %v, want io.EOF", pos, err)
		}
		if br.Position() != 1 {
			t.Errorf("Get(%d): Position changed: got %d, want 1", pos, br.Position())
		}
	}
	emptyBr := buf.NewBufReadonly(nil)
	_, err := emptyBr.Get(0)
	if !errors.Is(err, io.EOF) {
		t.Errorf("Get(0) from empty buffer: error got %v, want io.EOF", err)
	}
}

// TestBufReadonly_Bytes tests the Bytes method for BufReadonly, ensuring it returns the underlying
// byte slice correctly for nil, empty, and non-empty initial data. It also verifies that the returned
// slice is the original, not a copy, by testing aliasing.
func TestBufReadonly_Bytes(t *testing.T) {
	t.Run("NilData", func(t *testing.T) {
		br := buf.NewBufReadonly(nil)
		if br.Bytes() != nil {
			t.Errorf("Bytes() on nil data: got %q, want nil", br.Bytes())
		}
	})
	t.Run("EmptyData", func(t *testing.T) {
		br := buf.NewBufReadonly([]byte{})
		if !bytes.Equal(br.Bytes(), []byte{}) {
			t.Errorf("Bytes() on empty data: got %q, want []byte{}", br.Bytes())
		}
	})
	t.Run("NonEmptyData", func(t *testing.T) {
		data := []byte("hello")
		br := buf.NewBufReadonly(data) // br.bytes points to `data`
		retBytes := br.Bytes()
		if !bytes.Equal(retBytes, data) {
			t.Errorf("Bytes() on non-empty data: got %q, want %q", retBytes, data)
		}
		// Test that it's the same underlying array
		if len(data) > 0 && len(retBytes) > 0 {
			originalFirstByte := data[0]
			data[0] = 'X' // Modify original
			if retBytes[0] != 'X' {
				t.Error("Bytes() did not return the original slice (modification of original not reflected)")
			}
			data[0] = originalFirstByte // Restore
		}
	})
}

// TestBufReadonly_Len tests the Len method for BufReadonly, verifying it accurately reports
// the length of the underlying byte slice for nil, empty, and non-empty data.
func TestBufReadonly_Len(t *testing.T) {
	brNil := buf.NewBufReadonly(nil)
	if brNil.Len() != 0 {
		t.Errorf("Len() for nil data: got %d, want 0", brNil.Len())
	}
	brEmpty := buf.NewBufReadonly([]byte{})
	if brEmpty.Len() != 0 {
		t.Errorf("Len() for empty data: got %d, want 0", brEmpty.Len())
	}
	data := []byte("test")
	brData := buf.NewBufReadonly(data)
	if brData.Len() != len(data) {
		t.Errorf("Len() for data: got %d, want %d", brData.Len(), len(data))
	}
}

// TestBufReadonly_Peek tests the Peek method for BufReadonly, covering various scenarios including
// peeking different amounts of data, handling requests exceeding available data, peeking at/beyond the end,
// from negative positions, and with zero or negative counts for n.
// It ensures Peek does not advance the buffer's position.
func TestBufReadonly_Peek(t *testing.T) {
	data := []byte("0123456789")
	br := buf.NewBufReadonly(data)

	tests := []struct {
		name             string
		pos              int64
		peekN            int
		expectedPeekData []byte
		expectedErrStr   string
		expectedFinalPos int64
	}{
		{"PeekLessThanAvailable", 0, 3, []byte("012"), "", 0},
		{"PeekAllAvailableFromStart", 0, 10, []byte("0123456789"), "", 0},
		{"PeekMoreThanAvailableFromStart", 0, 15, []byte("0123456789"), "", 0},
		{"PeekFromMiddle", 5, 3, []byte("567"), "", 5},
		{"PeekMoreThanAvailableFromMiddle", 5, 10, []byte("56789"), "", 5},
		{"PeekAtEnd", 10, 5, nil, "EOF", 10},
		{"PeekBeyondEnd", 11, 5, nil, "EOF", 11},
		{"PeekNegativePosition", -1, 5, nil, "EOF", -1},
		{"PeekNIsZero", 2, 0, []byte{}, "", 2},
		{"PeekNIsNegative", 2, -1, nil, "argo.BufReadonly.Peek: count cannot be negative", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			br.SetPosition(tt.pos)
			peekedBytes, err := br.Peek(tt.peekN)

			if tt.expectedErrStr != "" {
				if err == nil {
					t.Errorf("Peek() error = nil, want %q", tt.expectedErrStr)
				} else if err.Error() != tt.expectedErrStr {
					if !(tt.expectedErrStr == "EOF" && errors.Is(err, io.EOF)) {
						t.Errorf("Peek() error = %q, want %q", err.Error(), tt.expectedErrStr)
					}
				}
			} else if err != nil {
				t.Errorf("Peek() unexpected error: %v", err)
			}

			if !bytes.Equal(peekedBytes, tt.expectedPeekData) {
				t.Errorf("Peek() data got %q, want %q", peekedBytes, tt.expectedPeekData)
			}
			if br.Position() != tt.expectedFinalPos {
				t.Errorf("Peek() final position got %d, want %d", br.Position(), tt.expectedFinalPos)
			}
		})
	}

	emptyBr := buf.NewBufReadonly(nil)
	_, err := emptyBr.Peek(1)
	if !errors.Is(err, io.EOF) {
		t.Errorf("Peek(1) from empty buffer: error got %v, want io.EOF", err)
	}
}

// TestInterfaceImplementations is a compile-time check to ensure that Buf and BufReadonly
// satisfy the intended buf.Read and buf.Write interfaces.
func TestInterfaceImplementations(t *testing.T) {
	var _ buf.Read = (*buf.Buf)(nil)
	var _ buf.Write = (*buf.Buf)(nil)
	var _ buf.Read = (*buf.BufReadonly)(nil)

	// This test doesn't have runtime assertions if the above lines compile.
	// If they don't compile, the test will fail during the build phase.
	t.Log("Interface assertions compiled successfully.")
}
