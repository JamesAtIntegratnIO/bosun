package prompt

import (
	"fmt"
	"sort"
	"strings"

	"github.com/JamesAtIntegratnIO/bosun/edits"
)

// File is one repository file the prompt may describe.
//
// Err records why a file could not be read, because the alternative is
// dropping it silently, and this prompt is also the evidence string
// edits.Policy.corroborated checks proposed versions against, so a file that
// quietly vanishes narrows what the applier will accept without saying so.
type File struct {
	Path string
	Data []byte
	Err  error
}

// UserInput is everything the user-side prompt is assembled from.
type UserInput struct {
	// Header is the opening line or lines: which pull request, and what moved.
	// Caller-supplied because the shipped path knows the artifact, the project
	// and the stage, and a fixture does not.
	Header string
	// Report is the gate's own report, verbatim.
	Report string
	// Files are the files this promotion may change; not everything the
	// repository holds. Order does not matter; they are sorted by path.
	Files []File
	// Inventory selects the scalar inventory over whole-file pasting.
	//
	// True on every shipped path. The scalar inventory is the important part:
	// handed one, a model chooses a key from a list; without one it invents a
	// key path and paraphrases a value, and the applier, correctly, throws the
	// result away. False exists for the eval suite's ablation, which measures
	// exactly that difference.
	Inventory bool
}

// User renders the user-side prompt.
//
// One implementation, in the package that owns the prompts, because the eval
// suite rebuilt this and the two had already diverged, the shipped prompt
// grew an artifact line and the copy being scored did not, so the score
// described a prompt nobody was given.
func User(in UserInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n%s\n\n", strings.TrimRight(in.Header, "\n"), in.Report)

	files := append([]File(nil), in.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	if !in.Inventory {
		b.WriteString("Repository files this pull request may change:\n\n")
		for _, f := range files {
			fmt.Fprintf(&b, "--- %s ---\n%s\n", f.Path, f.Data)
		}
		b.WriteString(closing)
		return b.String()
	}

	b.WriteString("Repository files this pull request may change.\n")
	b.WriteString("Use these keys and values EXACTLY as written.\n\n")
	var skipped []string
	for _, f := range files {
		if f.Err != nil {
			skipped = append(skipped, f.Path)
			continue
		}
		inv, err := edits.Inventory(f.Data, "")
		if err != nil {
			skipped = append(skipped, f.Path)
			continue
		}
		b.WriteString(edits.Render(f.Path, inv))
		b.WriteString("\n")
	}
	if len(skipped) > 0 {
		fmt.Fprintf(&b, "(%s could not be read and %s NOT described above: %s)\n\n",
			count(len(skipped), "file"), isAre(len(skipped)), strings.Join(skipped, ", "))
	}
	b.WriteString(closing)
	return b.String()
}

// closing is the instruction the model acts on, last so it is the most recent
// thing in the context.
const closing = "Classify this pull request and, if mechanical, give the edits."

func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}
