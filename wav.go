package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

type wavFormat struct {
	Channels      int
	SampleRate    int
	BitsPerSample int
	BlockAlign    int
	DataOffset    int64
	DataSize      int64
}

func (w wavFormat) durationSeconds() float64 {
	if w.BlockAlign <= 0 || w.SampleRate <= 0 {
		return 0
	}
	return float64(w.DataSize/int64(w.BlockAlign)) / float64(w.SampleRate)
}

// readWAVFormat walks the RIFF chunk list (CoreAudio writers emit extra
// chunks such as FLLR padding) and clamps the declared data length to the
// bytes actually present, so a header that was never finalized still exposes
// the full recording.
func readWAVFormat(path string) (wavFormat, error) {
	f, err := os.Open(path)
	if err != nil {
		return wavFormat{}, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return wavFormat{}, err
	}
	fileSize := info.Size()

	var riff [12]byte
	if _, err := io.ReadFull(f, riff[:]); err != nil {
		return wavFormat{}, fmt.Errorf("read RIFF header: %w", err)
	}
	if string(riff[0:4]) != "RIFF" || string(riff[8:12]) != "WAVE" {
		return wavFormat{}, errors.New("not a RIFF/WAVE file")
	}

	format := wavFormat{}
	haveFmt := false
	offset := int64(12)
	for offset+8 <= fileSize {
		var header [8]byte
		if _, err := f.ReadAt(header[:], offset); err != nil {
			return wavFormat{}, fmt.Errorf("read chunk header: %w", err)
		}
		chunkID := string(header[0:4])
		chunkSize := int64(binary.LittleEndian.Uint32(header[4:8]))
		body := offset + 8
		switch chunkID {
		case "fmt ":
			var fmtChunk [16]byte
			if _, err := f.ReadAt(fmtChunk[:], body); err != nil {
				return wavFormat{}, fmt.Errorf("read fmt chunk: %w", err)
			}
			if audioFormat := binary.LittleEndian.Uint16(fmtChunk[0:2]); audioFormat != 1 {
				return wavFormat{}, fmt.Errorf("unsupported WAV encoding %d (want PCM)", audioFormat)
			}
			format.Channels = int(binary.LittleEndian.Uint16(fmtChunk[2:4]))
			format.SampleRate = int(binary.LittleEndian.Uint32(fmtChunk[4:8]))
			format.BlockAlign = int(binary.LittleEndian.Uint16(fmtChunk[12:14]))
			format.BitsPerSample = int(binary.LittleEndian.Uint16(fmtChunk[14:16]))
			haveFmt = true
		case "data":
			format.DataOffset = body
			format.DataSize = chunkSize
			if format.DataSize <= 0 || body+format.DataSize > fileSize {
				format.DataSize = fileSize - body
			}
		}
		if chunkSize%2 == 1 {
			chunkSize++
		}
		offset = body + chunkSize
	}
	if !haveFmt || format.DataOffset == 0 {
		return wavFormat{}, errors.New("WAV file has no fmt or data chunk")
	}
	if format.BlockAlign <= 0 || format.SampleRate <= 0 || format.Channels <= 0 {
		return wavFormat{}, errors.New("WAV fmt chunk has invalid parameters")
	}
	format.DataSize -= format.DataSize % int64(format.BlockAlign)
	return format, nil
}

func writeWAVHeader(w io.Writer, format wavFormat, dataLen int64) error {
	byteRate := format.SampleRate * format.BlockAlign
	var header [44]byte
	copy(header[0:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], uint32(36+dataLen))
	copy(header[8:12], "WAVE")
	copy(header[12:16], "fmt ")
	binary.LittleEndian.PutUint32(header[16:20], 16)
	binary.LittleEndian.PutUint16(header[20:22], 1)
	binary.LittleEndian.PutUint16(header[22:24], uint16(format.Channels))
	binary.LittleEndian.PutUint32(header[24:28], uint32(format.SampleRate))
	binary.LittleEndian.PutUint32(header[28:32], uint32(byteRate))
	binary.LittleEndian.PutUint16(header[32:34], uint16(format.BlockAlign))
	binary.LittleEndian.PutUint16(header[34:36], uint16(format.BitsPerSample))
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:44], uint32(dataLen))
	_, err := w.Write(header[:])
	return err
}

type wavChunk struct {
	Path          string
	OffsetSeconds float64
}

var errWAVTooShortToSplit = errors.New("WAV is shorter than one chunk")

// splitWAVForTranscription slices a PCM WAV into standalone chunk files of at
// most chunkSeconds each, written next to the source with owner-only
// permissions. Callers own deleting the returned files.
func splitWAVForTranscription(path string, chunkSeconds float64) ([]wavChunk, error) {
	if chunkSeconds <= 0 {
		return nil, errors.New("chunkSeconds must be positive")
	}
	format, err := readWAVFormat(path)
	if err != nil {
		return nil, err
	}
	framesPerChunk := int64(chunkSeconds * float64(format.SampleRate))
	if framesPerChunk <= 0 {
		return nil, errors.New("chunk length is shorter than one frame")
	}
	bytesPerChunk := framesPerChunk * int64(format.BlockAlign)
	totalFrames := format.DataSize / int64(format.BlockAlign)
	if totalFrames <= framesPerChunk {
		return nil, errWAVTooShortToSplit
	}

	src, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	if _, err := src.Seek(format.DataOffset, io.SeekStart); err != nil {
		return nil, err
	}

	chunks := []wavChunk{}
	cleanup := func() {
		for _, c := range chunks {
			os.Remove(c.Path)
		}
	}
	remaining := format.DataSize
	for index := 0; remaining > 0; index++ {
		size := bytesPerChunk
		if size > remaining {
			size = remaining
		}
		chunkPath := fmt.Sprintf("%s.chunk%03d.wav", path, index)
		if err := writeWAVChunk(chunkPath, format, src, size); err != nil {
			cleanup()
			return nil, err
		}
		framesBefore := (format.DataSize - remaining) / int64(format.BlockAlign)
		chunks = append(chunks, wavChunk{
			Path:          chunkPath,
			OffsetSeconds: float64(framesBefore) / float64(format.SampleRate),
		})
		remaining -= size
	}
	return chunks, nil
}

func writeWAVChunk(path string, format wavFormat, src io.Reader, size int64) error {
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := writeWAVHeader(out, format, size); err != nil {
		out.Close()
		return err
	}
	if _, err := io.CopyN(out, src, size); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
