package rag

import "strings"

// ChunkText splits plain text (or Markdown) into overlapping chunks suitable
// for embedding and vector storage.
//
// chunkSize is a soft target measured in runes (not bytes). overlap is the
// number of trailing runes from each chunk that are prepended to the next
// chunk for context continuity.
//
// Input sanitisation:
//   - chunkSize < 50  → clamped to 50
//   - overlap < 0     → clamped to 0
//   - overlap >= chunkSize → clamped to chunkSize/4
func ChunkText(text string, chunkSize int, overlap int) []string {
	if chunkSize < 50 {
		chunkSize = 50
	}
	if overlap < 0 {
		overlap = 0
	}
	if overlap >= chunkSize {
		overlap = chunkSize / 4
	}

	if strings.TrimSpace(text) == "" {
		return nil
	}

	// Phase 1: split into paragraph candidates.
	candidates := splitIntoParagraphs(text)

	// Phase 2: merge small paragraphs and split large ones.
	chunks := mergeAndSplit(candidates, chunkSize)

	// Phase 3: apply overlap between consecutive chunks.
	return applyOverlap(chunks, overlap)
}

// splitIntoParagraphs splits text into paragraph-level candidates.
// Markdown headings (lines starting with #) are always their own candidates.
// Blank lines act as paragraph delimiters.
func splitIntoParagraphs(text string) []string {
	lines := strings.Split(text, "\n")
	var candidates []string
	var buf []string

	flush := func() {
		para := strings.TrimSpace(strings.Join(buf, "\n"))
		if para != "" {
			candidates = append(candidates, para)
		}
		buf = buf[:0]
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Markdown heading: flush current buffer, emit heading as its own candidate.
		if isMarkdownHeading(trimmed) {
			flush()
			candidates = append(candidates, trimmed)
			continue
		}

		// Blank line: flush current buffer.
		if trimmed == "" {
			flush()
			continue
		}

		buf = append(buf, trimmed)
	}
	flush()

	return candidates
}

func isMarkdownHeading(line string) bool {
	if len(line) < 2 {
		return false
	}
	i := 0
	for i < len(line) && line[i] == '#' {
		i++
	}
	return i >= 1 && i <= 6 && i < len(line) && line[i] == ' '
}

// mergeAndSplit greedily merges small paragraphs up to chunkSize, then splits
// oversized paragraphs on sentence boundaries.
func mergeAndSplit(candidates []string, chunkSize int) []string {
	var chunks []string
	current := ""

	for _, para := range candidates {
		paraRunes := []rune(para)
		curRunes := []rune(current)

		sep := ""
		if current != "" {
			sep = "\n\n"
		}
		combined := len(curRunes) + len([]rune(sep)) + len(paraRunes)

		if combined <= chunkSize {
			current = current + sep + para
		} else {
			if current != "" {
				chunks = append(chunks, splitLongText(current, chunkSize)...)
			}
			current = para
		}
	}

	if current != "" {
		chunks = append(chunks, splitLongText(current, chunkSize)...)
	}

	return chunks
}

// splitLongText splits text that exceeds chunkSize on sentence boundaries, then
// falls back to word boundaries if no sentence boundary is found.
func splitLongText(text string, chunkSize int) []string {
	runes := []rune(text)
	if len(runes) <= chunkSize {
		return []string{text}
	}

	// Try sentence-boundary splitting.
	sentences := splitSentences(text)
	if len(sentences) > 1 {
		return mergeSentences(sentences, chunkSize)
	}

	// Fall back: hard split at word boundary.
	return hardSplit(runes, chunkSize)
}

// splitSentences splits text on Chinese/English sentence terminators.
func splitSentences(text string) []string {
	var sentences []string
	var buf strings.Builder
	runes := []rune(text)

	for i, r := range runes {
		buf.WriteRune(r)
		// Sentence terminators followed by a space or Chinese punctuation.
		if r == '.' || r == '!' || r == '?' || r == '。' || r == '！' || r == '？' {
			next := i + 1
			if next >= len(runes) || runes[next] == ' ' || runes[next] == '\n' ||
				runes[next] == '。' || runes[next] == '！' || runes[next] == '？' {
				s := strings.TrimSpace(buf.String())
				if s != "" {
					sentences = append(sentences, s)
				}
				buf.Reset()
			}
		}
	}
	if remaining := strings.TrimSpace(buf.String()); remaining != "" {
		sentences = append(sentences, remaining)
	}

	return sentences
}

// mergeSentences greedily merges sentences up to chunkSize.
func mergeSentences(sentences []string, chunkSize int) []string {
	var chunks []string
	current := ""

	for _, s := range sentences {
		sep := ""
		if current != "" {
			sep = " "
		}
		candidate := current + sep + s
		if len([]rune(candidate)) <= chunkSize {
			current = candidate
		} else {
			if current != "" {
				chunks = append(chunks, current)
			}
			// Single sentence still too long: hard split it.
			if len([]rune(s)) > chunkSize {
				chunks = append(chunks, hardSplit([]rune(s), chunkSize)...)
				current = ""
			} else {
				current = s
			}
		}
	}

	if current != "" {
		chunks = append(chunks, current)
	}

	return chunks
}

// hardSplit splits runes at word boundaries (spaces) or hard at chunkSize.
func hardSplit(runes []rune, chunkSize int) []string {
	var chunks []string
	for len(runes) > 0 {
		if len(runes) <= chunkSize {
			chunks = append(chunks, string(runes))
			break
		}
		// Try to find a space to break at.
		cut := chunkSize
		for cut > chunkSize/2 && runes[cut] != ' ' && runes[cut] != '\n' {
			cut--
		}
		if runes[cut] == ' ' || runes[cut] == '\n' {
			cut++ // include the space in the left chunk, or trim below
		}
		chunks = append(chunks, strings.TrimSpace(string(runes[:cut])))
		runes = runes[cut:]
	}
	return chunks
}

// applyOverlap prepends the last `overlap` runes of chunk[i-1] to chunk[i].
func applyOverlap(chunks []string, overlap int) []string {
	if overlap == 0 || len(chunks) <= 1 {
		return chunks
	}

	result := make([]string, len(chunks))
	result[0] = chunks[0]

	for i := 1; i < len(chunks); i++ {
		prev := []rune(chunks[i-1])
		suffix := ""
		if len(prev) > overlap {
			suffix = string(prev[len(prev)-overlap:])
		} else {
			suffix = string(prev)
		}
		result[i] = suffix + chunks[i]
	}

	return result
}
