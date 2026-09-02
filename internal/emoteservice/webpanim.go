package emoteservice

import "encoding/binary"

// frameDurations reads an animated webp's per-frame durations in milliseconds by
// walking its RIFF chunks. Nothing else is exact: ImageMagick's %T quantises to
// centiseconds (5.7% of measured frames are not a multiple of 10 ms, enough to
// flip a verdict at the 3 Hz line), ffprobe reports a fabricated constant frame
// rate, and Go's webp decoder does not read animations at all. A still image has
// no ANMF chunks and yields nothing.
func frameDurations(data []byte) []int {
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return nil
	}

	end := len(data)
	if size := int(binary.LittleEndian.Uint32(data[4:8])) + 8; size >= 12 && size < end {
		end = size
	}

	var durations []int
	for off := 12; off+8 <= end; {
		fourcc := string(data[off : off+4])
		size := int(binary.LittleEndian.Uint32(data[off+4 : off+8]))
		payload := off + 8
		if size < 0 || payload+size > end {
			break
		}
		// An ANMF payload is 24-bit x, y, width and height, then the 24-bit
		// little-endian duration, then a flags byte.
		if fourcc == "ANMF" && size >= 16 {
			d := data[payload+12 : payload+15]
			durations = append(durations, int(d[0])|int(d[1])<<8|int(d[2])<<16)
		}
		off = payload + size + size&1
	}
	return durations
}
