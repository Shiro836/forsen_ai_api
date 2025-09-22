package ffmpeg_test

import (
	"app/pkg/ffmpeg"
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	_ "embed"

	"github.com/stretchr/testify/require"
)

//go:embed okayeg_ref.wav
var testAudio []byte

func TestAllFiltersWithApplyStringFilters(t *testing.T) {
	// Skip test if reference file doesn't exist
	if _, err := os.Stat("okayeg_ref.wav"); os.IsNotExist(err) {
		t.Skip("reference audio file not found")
	}

	assert := require.New(t)
	client := ffmpeg.New(&ffmpeg.Config{
		TmpDir: "/tmp",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create tmp directory if it doesn't exist
	os.MkdirAll("tmp", 0755)

	// Define all filter types with their names and numbers
	allFilters := []struct {
		name   string
		number int
	}{
		// Echo filters
		{"room_echo", 1},
		{"hall_echo", 2},
		{"outside_echo", 3},

		// Pitch filters
		{"pitch_down", 4},
		{"pitch_up", 5},

		// Quality filters
		{"telephone", 6},
		{"muffled", 7},
		{"quiet", 8},
		{"ghost", 9},
		{"chorus", 10},

		// Speed filters
		{"slower", 11},
		{"faster", 12},

		// Spatial filters
		{"right_side", 13},
		{"left_side", 14},
		{"left_to_right", 15},
		{"right_to_left", 16},
		{"quiet_to_loud", 17},
		{"loud_to_quiet", 18},

		// Background filters
		{"bog", 19},
		{"keyboard", 20},
		{"typewriter", 21},
		{"writing", 22},
		{"iphone", 23},
		{"cave", 24},
		{"hospital", 25},
		{"windy", 26},
		{"clock", 27},
		{"crackles", 28},
		{"crickets", 29},
		{"birds", 30},
		{"lava", 31},
	}

	// Test each filter individually using ApplyStringFilters
	for _, filter := range allFilters {
		t.Run(filter.name, func(t *testing.T) {
			filterNames := []string{fmt.Sprintf("%d", filter.number)}
			result, err := client.ApplyStringFilters(ctx, testAudio, filterNames)
			assert.NoError(err, "Filter %s (number %d) should not error", filter.name, filter.number)
			assert.NotEmpty(result, "Filter %s (number %d) should produce non-empty result", filter.name, filter.number)

			// Save the result to a file for inspection
			filename := fmt.Sprintf("tmp/%d_%s.mp3", filter.number, filter.name)
			err = os.WriteFile(filename, result, 0644)
			assert.NoError(err, "Should be able to write result file for %s", filter.name)
		})
	}

	// Test multiple filters applied in sequence
	t.Run("multiple_filters_sequence", func(t *testing.T) {
		// Test a sequence of different filter types
		filterSequence := []string{"1", "4", "6", "11", "13"} // room_echo, pitch_down, telephone, slower, right_side
		result, err := client.ApplyStringFilters(ctx, testAudio, filterSequence)
		assert.NoError(err, "Multiple filters sequence should not error")
		assert.NotEmpty(result, "Multiple filters sequence should produce non-empty result")

		// Save the result
		filename := "tmp/multiple_filters_sequence.mp3"
		err = os.WriteFile(filename, result, 0644)
		assert.NoError(err, "Should be able to write multiple filters result file")
	})

	// Test empty filter list
	t.Run("empty_filters", func(t *testing.T) {
		result, err := client.ApplyStringFilters(ctx, testAudio, []string{})
		assert.NoError(err, "Empty filter list should not error")
		assert.Equal(testAudio, result, "Empty filter list should return original audio")
	})

	// Test invalid filter numbers
	t.Run("invalid_filters", func(t *testing.T) {
		// Test invalid filter number (too high)
		_, err := client.ApplyStringFilters(ctx, testAudio, []string{"999"})
		assert.Error(err, "Invalid filter number should error")

		// Test invalid filter number (too low)
		_, err = client.ApplyStringFilters(ctx, testAudio, []string{"0"})
		assert.Error(err, "Invalid filter number should error")

		// Test non-numeric filter name
		_, err = client.ApplyStringFilters(ctx, testAudio, []string{"invalid"})
		assert.Error(err, "Non-numeric filter name should error")
	})

	// Test applying all filters in one call
	t.Run("all_filters_at_once", func(t *testing.T) {
		// Create a list of all filter numbers as strings
		allFilterNumbers := make([]string, 0, 31)
		for i := 1; i <= 31; i++ {
			allFilterNumbers = append(allFilterNumbers, fmt.Sprintf("%d", i))
		}

		// Apply all filters in one call
		result, err := client.ApplyStringFilters(ctx, testAudio, allFilterNumbers)
		assert.NoError(err, "Applying all filters at once should not error")
		assert.NotEmpty(result, "Applying all filters at once should produce non-empty result")

		// Save the result for inspection
		filename := "tmp/all_filters_at_once.mp3"
		err = os.WriteFile(filename, result, 0644)
		assert.NoError(err, "Should be able to write all filters result file")

		// Verify the result is different from the original (since we applied many filters)
		assert.NotEqual(testAudio, result, "Result should be different from original after applying all filters")
	})
}
