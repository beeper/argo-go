// Package bitset_test contains tests for the bitset package.
package bitset_test

import (
	"bytes"
	"fmt"
	"math/big"
	"testing"

	"github.com/beeper/argo-go/pkg/bitset"
	"github.com/beeper/argo-go/pkg/buf"
)

// bigIntToBitSet converts a *big.Int to a *bitset.BitSet for testing purposes.
// It iterates through the bits of the *big.Int (up to its bit length)
// and sets the corresponding bits in the new BitSet.
// The LSB of the *big.Int corresponds to index 0 in the BitSet.
func bigIntToBitSet(val *big.Int) *bitset.BitSet {
	bs := bitset.NewBitSet()
	if val.Sign() == 0 { // big.Int 0
		return bs // An empty bitset represents 0
	}

	// val.BitLen() is the number of bits required to represent val.
	for i := 0; i < val.BitLen(); i++ {
		if val.Bit(i) == 1 { // big.Int.Bit(i) gets the i-th bit (0-indexed)
			bs.SetBit(i)
		}
	}
	return bs
}

// bigIntFromBitSet converts a *bitset.BitSet back to a *big.Int.
// This is primarily used in tests for logging and verifying that the
// BitSet's internal *big.Int value matches an expected *big.Int.
// It relies on bs.Bytes() returning the big-endian byte representation
// of the BitSet's underlying *big.Int.
func bigIntFromBitSet(bs *bitset.BitSet) *big.Int {
	return new(big.Int).SetBytes(bs.Bytes())
}

// TestVarBitSetRoundTrip tests the VarBitSet.Write and VarBitSet.Read methods
// to ensure that a BitSet can be serialized to its variable-length byte format
// and then deserialized back to an equivalent BitSet.
// It covers various integer values, including zero, small numbers, and larger numbers
// that would span multiple bytes in the variable-length encoding.
func TestVarBitSetRoundTrip(t *testing.T) {
	testCases := []struct {
		name string
		val  *big.Int
	}{
		{"0n", big.NewInt(0)},
		{"1n", big.NewInt(1)},
		{"127n", big.NewInt(127)},
		{"128n", big.NewInt(128)},
		{"255n", big.NewInt(255)},
		{"256n", big.NewInt(256)},
		{"0xffffffn", big.NewInt(0xffffff)}, // 16777215
	}

	varOps := bitset.VarBitSet{}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			initialBs := bigIntToBitSet(tc.val)

			// Write the BitSet using VarBitSet.Write. No padding is used for these test cases.
			writtenBytes, err := varOps.Write(initialBs, 0)
			if err != nil {
				t.Fatalf("VarBitSet.Write for value %s failed: %v", tc.val.String(), err)
			}

			// Read the BitSet back using VarBitSet.Read.
			// A buf.BufReadonly is used to provide the buf.Read interface needed by varOps.Read.
			bufReader := buf.NewBufReadonly(writtenBytes)
			_, readBs, err := varOps.Read(bufReader)
			if err != nil {
				t.Fatalf("VarBitSet.Read for value %s (source bytes %x) failed: %v", tc.val.String(), writtenBytes, err)
			}

			// Compare the original BitSet's bytes with the read BitSet's bytes.
			if !bytes.Equal(initialBs.Bytes(), readBs.Bytes()) {
				t.Errorf("Round trip for value %s failed.\nInitial BitSet bytes: %x (value: %s)\nRead BitSet bytes:    %x (value: %s)",
					tc.val.String(), initialBs.Bytes(), tc.val.String(),
					readBs.Bytes(), bigIntFromBitSet(readBs).String())
			}
		})
	}
}

// TestBitSettingRoundTrip verifies the core bit manipulation methods of a BitSet:
// GetBit, SetBit, and UnsetBit. It ensures that after setting a bit, it reads as set;
// after unsetting it, it reads as unset; and that the BitSet can be restored to its
// original state. This is tested for a range of initial BitSet values and bit positions.
// The test iterates through initial integer values (0 to 64) to create initial BitSets,
// and for each, it tests manipulating bits at positions 0 to 64.
func TestBitSettingRoundTrip(t *testing.T) {
	const maxTestBit = 65 // Test bit indices up to 64, covering values representable by a uint64.

	for iVal := 0; iVal < maxTestBit; iVal++ { // Represents the initial integer value for the BitSet.
		for bitNum := 0; bitNum < maxTestBit; bitNum++ { // Represents the bit index to test.
			initialValInt64 := int64(iVal)

			t.Run(fmt.Sprintf("val_%d_bit_%d", initialValInt64, bitNum), func(t *testing.T) {
				initialBigInt := big.NewInt(initialValInt64)
				originalBs := bigIntToBitSet(initialBigInt) // BitSet representing the initial value.

				wasSet := originalBs.GetBit(bitNum)

				currentBsState := bigIntToBitSet(initialBigInt) // Create a working copy.
				currentBsState.SetBit(bitNum)

				if !currentBsState.GetBit(bitNum) {
					t.Errorf("Value %s, Bit %d: After SetBit, GetBit returned false. Expected true. BitSet bytes: %x",
						initialBigInt.String(), bitNum, currentBsState.Bytes())
				}

				currentBsState.UnsetBit(bitNum)

				if currentBsState.GetBit(bitNum) {
					t.Errorf("Value %s, Bit %d: After UnsetBit, GetBit returned true. Expected false. BitSet bytes: %x",
						initialBigInt.String(), bitNum, currentBsState.Bytes())
				}

				if wasSet {
					currentBsState.SetBit(bitNum)
				}

				if !bytes.Equal(currentBsState.Bytes(), originalBs.Bytes()) {
					t.Errorf("Value %s, Bit %d: Round trip mismatch.\nOriginal BitSet bytes: %x (value: %s, wasSet: %t)\nFinal BitSet bytes:    %x (value: %s)",
						initialBigInt.String(), bitNum, originalBs.Bytes(), initialBigInt.String(), wasSet,
						currentBsState.Bytes(), bigIntFromBitSet(currentBsState).String())
				}
			})
		}
	}
}

// TestFixedBitSetRoundTrip tests the FixedBitSet.Write and FixedBitSet.Read methods.
// It ensures that a BitSet can be serialized to its fixed-length byte format
// and then deserialized back to an equivalent BitSet. The test uses various
// integer values and calculates the required byte padding for the fixed-length encoding
// based on the bit length of the value.
func TestFixedBitSetRoundTrip(t *testing.T) {
	testCases := []struct {
		name string
		val  *big.Int
	}{
		{"0n", big.NewInt(0)},
		{"1n", big.NewInt(1)},
		{"127n", big.NewInt(127)},
		{"128n", big.NewInt(128)},
		{"255n", big.NewInt(255)},
		{"256n", big.NewInt(256)},
		{"0xffffffn", big.NewInt(0xffffff)}, // 16777215
	}

	fixedOps := bitset.FixedBitSet{}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			initialBs := bigIntToBitSet(tc.val)

			// Determine padding based on the number of bits in the value.
			numBitsInVal := tc.val.BitLen()
			// If value is 0, BitLen is 0. BytesNeededForNumBits(0) is 0.
			// FixedBitSet.Write for 0 with padToLength 0 results in one byte {0x00}.
			// So, if numBitsInVal is 0, we should explicitly set padToLength to 1
			// to ensure we write at least one byte for the zero value to match FixedBitSet.Write behavior.
			padToLength := fixedOps.BytesNeededForNumBits(numBitsInVal)
			if numBitsInVal == 0 && padToLength == 0 { // Special case for value 0
				padToLength = 1
			}

			writtenBytes, err := fixedOps.Write(initialBs, padToLength)
			if err != nil {
				t.Fatalf("FixedBitSet.Write for value %s (padToLength %d) failed: %v",
					tc.val.String(), padToLength, err)
			}

			readBs, err := fixedOps.Read(writtenBytes, 0, len(writtenBytes))
			if err != nil {
				t.Fatalf("FixedBitSet.Read for value %s (data %x, reading %d bytes from pos 0) failed: %v",
					tc.val.String(), writtenBytes, len(writtenBytes), err)
			}

			if !bytes.Equal(initialBs.Bytes(), readBs.Bytes()) {
				t.Errorf("Round trip for value %s failed.\nInitial BitSet bytes: %x (value: %s)\nRead BitSet bytes:    %x (value: %s)",
					tc.val.String(), initialBs.Bytes(), tc.val.String(),
					readBs.Bytes(), bigIntFromBitSet(readBs).String())
			}
		})
	}
}
