package varint_test

import (
	"fmt"
	"math/big"
	"testing"

	"github.com/beeper/argo-go/pkg/varint" // The varint package to be used
)

// testBigIntValues is a direct translation of the TypeScript testValues array.
// It's initialized in an init function for clarity, especially with large numbers.
var testBigIntValues []*big.Int

func init() {
	// Helper to panic on bad string conversion during test setup.
	// This is acceptable for test data initialization.
	mustSetString := func(s string, base int) *big.Int {
		n, ok := new(big.Int).SetString(s, base)
		if !ok {
			panic(fmt.Sprintf("big.Int SetString failed for value: %s with base %d", s, base))
		}
		return n
	}

	testBigIntValues = []*big.Int{
		big.NewInt(0),
		big.NewInt(1),
		big.NewInt(127),
		big.NewInt(128),
		big.NewInt(255),
		big.NewInt(256),
		big.NewInt(624485),
		mustSetString("10000000000000000000000", 10),
	}
}

// TestUnsignedRoundTrip corresponds to the 'unsigned: round trip through bytes' test in TypeScript.
func TestUnsignedRoundTrip(t *testing.T) {
	for _, n := range testBigIntValues {
		// Create a sub-test for each value for better test output and isolation.
		t.Run(fmt.Sprintf("value_%s", n.String()), func(t *testing.T) {
			encodedBytes := varint.UnsignedEncode(n)

			decodedResult, numBytesRead, err := varint.UnsignedDecode(encodedBytes, 0)

			if err != nil {
				t.Fatalf("UnsignedDecode failed for %s: %v. Encoded: %x", n.String(), err, encodedBytes)
			}

			if numBytesRead != len(encodedBytes) {
				t.Errorf("UnsignedDecode read %d bytes, but encoded length was %d for %s. Encoded: %x",
					numBytesRead, len(encodedBytes), n.String(), encodedBytes)
			}

			if decodedResult.Cmp(n) != 0 {
				t.Errorf("Unsigned round trip failed for %s: expected %s, got %s",
					n.String(), n.String(), decodedResult.String())
			}
		})
	}
}

// TestZigZagRoundTrip corresponds to the 'zigzag: round trip through bytes' test in TypeScript.
func TestZigZagRoundTrip(t *testing.T) {
	for _, originalN := range testBigIntValues {
		// Test case for positive n (or n itself from testBigIntValues)
		t.Run(fmt.Sprintf("positive_value_%s", originalN.String()), func(t *testing.T) {
			encodedBytes := varint.ZigZagEncode(originalN)

			decodedResult, numBytesRead, err := varint.ZigZagDecode(encodedBytes, 0)

			if err != nil {
				t.Fatalf("ZigZagDecode failed for positive %s: %v. Encoded: %x", originalN.String(), err, encodedBytes)
			}

			if numBytesRead != len(encodedBytes) {
				t.Errorf("ZigZagDecode read %d bytes, but encoded length was %d for positive %s. Encoded: %x",
					numBytesRead, len(encodedBytes), originalN.String(), encodedBytes)
			}

			if decodedResult.Cmp(originalN) != 0 {
				t.Errorf("ZigZag round trip failed for positive %s: expected %s, got %s",
					originalN.String(), originalN.String(), decodedResult.String())
			}
		})

		// Test case for negative n
		// In TypeScript: testValues.forEach(n => expect(writeRead(-n)).toEqual(-n))
		negN := new(big.Int).Neg(originalN) // Create -originalN

		// Subtest name uses originalN for context, indicating it's the negative version.
		t.Run(fmt.Sprintf("negative_of_value_%s", originalN.String()), func(t *testing.T) {
			encodedBytes := varint.ZigZagEncode(negN)
			decodedResult, numBytesRead, err := varint.ZigZagDecode(encodedBytes, 0)

			if err != nil {
				// %s for big.Int automatically calls its String() method.
				t.Fatalf("ZigZagDecode failed for %s (negative of %s): %v. Encoded: %x", negN, originalN, err, encodedBytes)
			}

			if numBytesRead != len(encodedBytes) {
				t.Errorf("ZigZagDecode read %d bytes, but encoded length was %d for %s (negative of %s). Encoded: %x",
					numBytesRead, len(encodedBytes), negN, originalN, encodedBytes)
			}

			if decodedResult.Cmp(negN) != 0 {
				t.Errorf("ZigZag round trip failed for %s (negative of %s): expected %s, got %s",
					negN, originalN, negN, decodedResult.String())
			}
		})
	}
}

// TestUnsignedDecodeTooLong tests the error condition for varints exceeding the 37-byte limit.
func TestUnsignedDecodeTooLong(t *testing.T) {
	// Create a 39-byte varint sequence. All bytes have continuation bit set, last byte is a value.
	// This should trigger the "varint data exceeds 37-byte limit" error.
	tooLongBuf := make([]byte, 39)
	for i := 0; i < 38; i++ {
		tooLongBuf[i] = 0x80 // Continuation bit set
	}
	tooLongBuf[38] = 0x01 // Last byte, value 1

	_, _, err := varint.UnsignedDecode(tooLongBuf, 0)
	if err == nil {
		t.Fatal("UnsignedDecode did not return an error for a 39-byte varint")
	}
	expectedErr := "varint: varint data exceeds 37-byte limit (expected for up to 256-bit numbers)"
	if err.Error() != expectedErr {
		t.Errorf("UnsignedDecode error mismatch for too long varint:\nExpected: %s\nGot: %s", expectedErr, err.Error())
	}
}

// TestUnsignedDecodeShortBuffer tests decoding from a buffer that's too short for the varint.
func TestUnsignedDecodeShortBuffer(t *testing.T) {
	shortBuf := []byte{0x81} // Represents a multi-byte varint, but buffer ends prematurely.
	_, _, err := varint.UnsignedDecode(shortBuf, 0)
	if err == nil {
		t.Fatal("UnsignedDecode did not return an error for a short buffer")
	}
	expectedErr := "varint: buffer too short for UnsignedDecode"
	if err.Error() != expectedErr {
		t.Errorf("UnsignedDecode error mismatch for short buffer:\nExpected: %s\nGot: %s", expectedErr, err.Error())
	}

	// Test with an empty buffer
	_, _, err = varint.UnsignedDecode([]byte{}, 0)
	if err == nil {
		t.Fatal("UnsignedDecode did not return an error for an empty buffer")
	}
	if err.Error() != expectedErr { // Should also be "buffer too short"
		t.Errorf("UnsignedDecode error mismatch for empty buffer:\nExpected: %s\nGot: %s", expectedErr, err.Error())
	}
}

// TestUnsignedEncodeInto_PanicSmallBuffer tests that UnsignedEncodeInto panics with a small buffer.
func TestUnsignedEncodeInto_PanicSmallBuffer(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("UnsignedEncodeInto did not panic with too small buffer")
		} else {
			expectedPanic := "varint: buffer too small for UnsignedEncodeInto"
			if r != expectedPanic {
				t.Errorf("UnsignedEncodeInto panic message mismatch:\nExpected: %s\nGot: %v", expectedPanic, r)
			}
		}
	}()
	val := big.NewInt(128) // Requires 2 bytes
	buf := make([]byte, 1) // Buffer is too small
	varint.UnsignedEncodeInto(val, buf, 0)
}

// TestZigZagEncodeInto_PanicSmallBuffer tests that ZigZagEncodeInto panics with a small buffer.
func TestZigZagEncodeInto_PanicSmallBuffer(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("ZigZagEncodeInto did not panic with too small buffer")
		} else {
			// ZigZagEncodeInto calls UnsignedEncodeInto, so panic message is the same.
			expectedPanic := "varint: buffer too small for UnsignedEncodeInto"
			if r != expectedPanic {
				t.Errorf("ZigZagEncodeInto panic message mismatch:\nExpected: %s\nGot: %v", expectedPanic, r)
			}
		}
	}()
	val := big.NewInt(64)  // ZigZag(64) is 128, requires 2 bytes
	buf := make([]byte, 1) // Buffer is too small
	varint.ZigZagEncodeInto(val, buf, 0)
}
