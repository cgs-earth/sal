package pkg

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseByteSizeReadsGOMEMLIMITSizes(t *testing.T) {
	for value, want := range map[string]uint64{
		"8GiB":    8 << 30,
		"512MiB":  512 << 20,
		"1.5GiB":  3 << 29,
		"2048":    2048,
		"64KiB":   64 << 10,
		"1TiB":    1 << 40,
		"100B":    100,
		" 4 GiB ": 0,
	} {
		got, err := ParseByteSize(value)
		if want == 0 {
			require.Error(t, err, value)
			continue
		}
		require.NoError(t, err, value)
		require.Equal(t, want, got, value)
	}
}

func TestParseByteSizeRefusesWhatIsNotASize(t *testing.T) {
	_, err := ParseByteSize("lots")
	require.Error(t, err)
	_, err = ParseByteSize("-1GiB")
	require.Error(t, err)
}

// SAL_MEMORY_LIMIT replaces whatever budget the machine would give, and one
// that is not a size is ignored in favour of the detected budget.
func TestBudgetIsSALMemoryLimitWhenSet(t *testing.T) {
	t.Setenv(MemoryLimitEnv, "3GiB")
	require.Equal(t, MemoryBudget{Bytes: 3 << 30, Source: MemoryLimitEnv}, detectBudget())

	t.Setenv(MemoryLimitEnv, "plenty")
	detected := detectBudget()
	require.NotEqual(t, MemoryLimitEnv, detected.Source)
	require.NotZero(t, detected.Bytes, "a machine always has some memory to plan for")
}

func TestFormatBytesUsesBinaryUnits(t *testing.T) {
	require.Equal(t, "512 B", FormatBytes(512))
	require.Equal(t, "1.5 GiB", FormatBytes(3<<29))
	require.Equal(t, "8.0 MiB", FormatBytes(8<<20))
}
