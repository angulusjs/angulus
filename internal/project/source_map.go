package project

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

type sourceMapPoint struct {
	Generated int
	Source    string
	Original  int
}

type sourceMapDocument struct {
	Version        int      `json:"version"`
	Sources        []string `json:"sources"`
	SourcesContent []string `json:"sourcesContent"`
	Names          []string `json:"names"`
	Mappings       string   `json:"mappings"`
}

type sourceMapPosition struct {
	line   int
	column int
}

func sourceMapPositions(source string) []sourceMapPosition {
	positions := make([]sourceMapPosition, utf16Length(source)+1)
	position := sourceMapPosition{}
	units := 0
	for _, r := range source {
		positions[units] = position
		runeUnits := utf16.RuneLen(r)
		if r == '\n' {
			position.line++
			position.column = 0
		} else {
			position.column += runeUnits
		}
		for offset := units + 1; offset <= units+runeUnits; offset++ {
			positions[offset] = position
		}
		units += runeUnits
	}
	positions[units] = position
	return positions
}

func sourceMapPositionAt(positions []sourceMapPosition, offset int) sourceMapPosition {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(positions) {
		offset = len(positions) - 1
	}
	return positions[offset]
}

func utf16ByteOffsets(source string) []int {
	offsets := make([]int, len(source)+1)
	units := 0
	for start, r := range source {
		size := utf8.RuneLen(r)
		for offset := start; offset < start+size; offset++ {
			offsets[offset] = units
		}
		units += utf16.RuneLen(r)
		offsets[start+size] = units
	}
	return offsets
}

func sourceMapPath(mapPath, sourcePath string) string {
	relative, err := filepath.Rel(filepath.Dir(mapPath), sourcePath)
	if err != nil {
		relative = sourcePath
	}
	return filepath.ToSlash(relative)
}

func inlineSourceMap(generatedPath, generated string, points []sourceMapPoint, sourceContents map[string]string) string {
	if len(points) == 0 {
		return generated
	}
	sort.SliceStable(points, func(i, j int) bool {
		return points[i].Generated < points[j].Generated
	})

	doc := sourceMapDocument{Version: 3, Sources: []string{}, SourcesContent: []string{}, Names: []string{}}
	sourceIndexes := make(map[string]int)
	sourcePositions := make(map[string][]sourceMapPosition)
	generatedPositions := sourceMapPositions(generated)
	positionForSource := func(path string) []sourceMapPosition {
		if positions, ok := sourcePositions[path]; ok {
			return positions
		}
		positions := sourceMapPositions(sourceContents[path])
		sourcePositions[path] = positions
		return positions
	}
	indexFor := func(path string) int {
		name := sourceMapPath(generatedPath, path)
		if index, ok := sourceIndexes[name]; ok {
			return index
		}
		index := len(doc.Sources)
		sourceIndexes[name] = index
		doc.Sources = append(doc.Sources, name)
		doc.SourcesContent = append(doc.SourcesContent, sourceContents[path])
		return index
	}

	var mappings strings.Builder
	generatedLine, previousGeneratedColumn := 0, 0
	previousSource, previousOriginalLine, previousOriginalColumn := 0, 0, 0
	firstSegment := true
	for _, point := range points {
		if point.Generated < 0 {
			continue
		}
		generatedPosition := sourceMapPositionAt(generatedPositions, point.Generated)
		if generatedPosition.line != generatedLine {
			for line := generatedLine; line < generatedPosition.line; line++ {
				mappings.WriteByte(';')
			}
			generatedLine = generatedPosition.line
			previousGeneratedColumn = 0
			firstSegment = true
		}
		if !firstSegment {
			mappings.WriteByte(',')
		}
		firstSegment = false

		sourceIndex := indexFor(point.Source)
		original := sourceMapPositionAt(positionForSource(point.Source), point.Original)
		writeVLQ(&mappings, generatedPosition.column-previousGeneratedColumn)
		writeVLQ(&mappings, sourceIndex-previousSource)
		writeVLQ(&mappings, original.line-previousOriginalLine)
		writeVLQ(&mappings, original.column-previousOriginalColumn)
		previousGeneratedColumn = generatedPosition.column
		previousSource = sourceIndex
		previousOriginalLine = original.line
		previousOriginalColumn = original.column
	}
	doc.Mappings = mappings.String()
	data, err := json.Marshal(doc)
	if err != nil {
		return generated
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	if strings.HasSuffix(generatedPath, ".css") {
		return generated + "\n/*# sourceMappingURL=data:application/json;base64," + encoded + " */\n"
	}
	return generated + "\n//# sourceMappingURL=data:application/json;base64," + encoded + "\n"
}

func writeVLQ(output *strings.Builder, value int) {
	encoded := value << 1
	if value < 0 {
		encoded = (-value << 1) | 1
	}
	for {
		digit := encoded & 31
		encoded >>= 5
		if encoded != 0 {
			digit |= 32
		}
		output.WriteByte(base64VLQAlphabet[digit])
		if encoded == 0 {
			return
		}
	}
}

const base64VLQAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
