package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Reading Kargo, for the pipeline supervisor.
//
// Two of this package's three rules hold here unchanged: only GET and LIST,
// and no vendored types. The last earns its keep here more than anywhere
// else, these structs name eleven fields out of Kargo's CRDs, and a release
// that adds a field to any of them cannot break this build.
//
// The third rule is deliberately inverted, and the file used to claim
// otherwise. Reader's methods answer with Known/Note because their caller is
// rendering one line of a brief and has nothing useful to say about a failure.
// These three return an error instead, because their caller is the sweep, and
// pipeline.Collector turns each failure into a note naming the detector that
// therefore did not run ("promotions could not be read, so a wedged Stage
// would not have been found"). Deciding that is the sweep's job, not this
// file's, so the failure has to reach it intact.

// KargoStage is a Stage, reduced.
type KargoStage struct {
	Name           string
	Namespace      string
	CurrentFreight string
	Updates        []KargoUpdate
	Ready          bool
	ReadyReason    string
	ReadyMessage   string
	ReadySince     time.Duration
	// VerificationID is the id of the newest verification for the current
	// freight. It is what `kargo.akuity.io/reverify` takes, and without it the
	// remedy for a stuck verification is a paragraph instead of a command.
	VerificationID    string
	VerificationPhase string
	// VerificationRunNamespace and VerificationRunName point at the
	// AnalysisRun that verification is. Kargo writes the reference down, so
	// the run can be read directly instead of guessed at from labels, and
	// without it the only thing a finding can say about a stopped Stage is
	// that it stopped.
	VerificationRunNamespace string
	VerificationRunName      string
}

// KargoUpdate is one `yaml-update` step's file and keys.
type KargoUpdate struct {
	Path string
	Keys []string
}

type KargoWarehouse struct {
	Name         string
	Namespace    string
	Interval     time.Duration
	DiscoveredAt time.Time
	Ready        bool
	ReadyReason  string
	ReadyMessage string
	Latest       string
}

type KargoPromotion struct {
	Name      string
	Namespace string
	Stage     string
	Freight   string
	Phase     string
	StartedAt time.Time
	CreatedAt time.Time
	Message   string
}

// KargoAvailable reports whether this cluster serves Kargo at all, so a
// supervisor can say "Kargo is not installed here" rather than "no Stages
// found", which are different sentences with different actions.
func (a *APIServer) KargoAvailable(ctx context.Context) bool {
	return a.get(ctx, readStages.groupRoot(), nil) == nil
}

// Stages lists every Stage the agent may see.
//
// The `yaml-update` steps are the interesting part and the reason this reads
// the promotion template at all: they are the authoritative list of which
// files and keys promotions rewrite. Reading the same information
// out of the repository's Kargo values would answer a different question,
// what the target list says, and the two diverge exactly when something is
// wrong.
func (a *APIServer) Stages(ctx context.Context) ([]KargoStage, error) {
	var raw struct {
		Items []struct {
			Metadata meta `json:"metadata"`
			Spec     struct {
				PromotionTemplate struct {
					Spec struct {
						Steps []struct {
							Uses   string `json:"uses"`
							Config struct {
								Path    string `json:"path"`
								Updates []struct {
									Key string `json:"key"`
								} `json:"updates"`
							} `json:"config"`
						} `json:"steps"`
					} `json:"spec"`
				} `json:"promotionTemplate"`
			} `json:"spec"`
			Status struct {
				FreightHistory []struct {
					Items map[string]struct {
						Name string `json:"name"`
					} `json:"items"`
					VerificationHistory []struct {
						ID          string `json:"id"`
						Phase       string `json:"phase"`
						AnalysisRun struct {
							Namespace string `json:"namespace"`
							Name      string `json:"name"`
						} `json:"analysisRun"`
					} `json:"verificationHistory"`
				} `json:"freightHistory"`
				Conditions []condition `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := a.listAll(ctx, readStages, &raw); err != nil {
		return nil, err
	}
	out := make([]KargoStage, 0, len(raw.Items))
	for _, it := range raw.Items {
		st := KargoStage{Name: it.Metadata.Name, Namespace: it.Metadata.Namespace}
		for _, s := range it.Spec.PromotionTemplate.Spec.Steps {
			if s.Uses != "yaml-update" || s.Config.Path == "" {
				continue
			}
			u := KargoUpdate{Path: s.Config.Path}
			for _, up := range s.Config.Updates {
				if up.Key != "" {
					u.Keys = append(u.Keys, up.Key)
				}
			}
			if len(u.Keys) > 0 {
				st.Updates = append(st.Updates, u)
			}
		}
		if len(it.Status.FreightHistory) > 0 {
			for _, v := range it.Status.FreightHistory[0].Items {
				st.CurrentFreight = v.Name
				break
			}
			if vh := it.Status.FreightHistory[0].VerificationHistory; len(vh) > 0 {
				st.VerificationID, st.VerificationPhase = vh[0].ID, vh[0].Phase
				st.VerificationRunNamespace = vh[0].AnalysisRun.Namespace
				st.VerificationRunName = vh[0].AnalysisRun.Name
				// Kargo has recorded the run without its namespace in
				// releases that create it beside the Stage. Defaulting is
				// right where guessing a name would not be: the Stage's own
				// namespace is where Kargo puts these, and a wrong guess
				// 404s into a note rather than into a false sentence.
				if st.VerificationRunNamespace == "" {
					st.VerificationRunNamespace = it.Metadata.Namespace
				}
			}
		}
		if c, ok := findCondition(it.Status.Conditions, "Ready"); ok {
			st.Ready = c.Status == "True"
			st.ReadyReason, st.ReadyMessage = c.Reason, c.Message
			if !c.LastTransitionTime.IsZero() {
				st.ReadySince = time.Since(c.LastTransitionTime)
			}
		}
		out = append(out, st)
	}
	return out, nil
}

// Warehouses lists every Warehouse, with the freight it last discovered.
//
// The discovery timestamp is the point: a Warehouse that is Ready and has
// discovered nothing since last week is the failure that produces no event and
// no error, which is exactly what the sweep exists to find.
func (a *APIServer) Warehouses(ctx context.Context) ([]KargoWarehouse, error) {
	var raw struct {
		Items []struct {
			Metadata meta `json:"metadata"`
			Spec     struct {
				Interval string `json:"interval"`
			} `json:"spec"`
			Status struct {
				Conditions          []condition `json:"conditions"`
				DiscoveredArtifacts struct {
					DiscoveredAt time.Time `json:"discoveredAt"`
					Charts       []struct {
						Versions []string `json:"versions"`
					} `json:"charts"`
					Images []struct {
						References []struct {
							Tag string `json:"tag"`
						} `json:"references"`
					} `json:"images"`
				} `json:"discoveredArtifacts"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := a.listAll(ctx, readWarehouses, &raw); err != nil {
		return nil, err
	}
	out := make([]KargoWarehouse, 0, len(raw.Items))
	for _, it := range raw.Items {
		w := KargoWarehouse{
			Name:         it.Metadata.Name,
			Namespace:    it.Metadata.Namespace,
			DiscoveredAt: it.Status.DiscoveredArtifacts.DiscoveredAt,
		}
		// An unparseable or absent interval leaves this zero, which disables
		// the staleness check for this Warehouse rather than inventing a
		// threshold for it.
		if d, err := time.ParseDuration(it.Spec.Interval); err == nil {
			w.Interval = d
		}
		if c, ok := findCondition(it.Status.Conditions, "Ready"); ok {
			w.Ready = c.Status == "True"
			w.ReadyReason, w.ReadyMessage = c.Reason, c.Message
		}
		for _, ch := range it.Status.DiscoveredArtifacts.Charts {
			if len(ch.Versions) > 0 {
				w.Latest = ch.Versions[0]
				break
			}
		}
		if w.Latest == "" {
			for _, im := range it.Status.DiscoveredArtifacts.Images {
				if len(im.References) > 0 {
					w.Latest = im.References[0].Tag
					break
				}
			}
		}
		out = append(out, w)
	}
	return out, nil
}

// Promotions lists every Promotion, newest last.
//
// Both timestamps are carried because a Pending promotion has no StartedAt, it
// has not begun, and its age is the only thing that distinguishes a queue that
// is moving from one that has stopped.
func (a *APIServer) Promotions(ctx context.Context) ([]KargoPromotion, error) {
	var raw struct {
		Items []struct {
			Metadata meta `json:"metadata"`
			Spec     struct {
				Stage   string `json:"stage"`
				Freight string `json:"freight"`
			} `json:"spec"`
			Status struct {
				Phase     string    `json:"phase"`
				Message   string    `json:"message"`
				StartedAt time.Time `json:"startedAt"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := a.listAll(ctx, readPromotions, &raw); err != nil {
		return nil, err
	}
	out := make([]KargoPromotion, 0, len(raw.Items))
	for _, it := range raw.Items {
		out = append(out, KargoPromotion{
			Name:      it.Metadata.Name,
			Namespace: it.Metadata.Namespace,
			Stage:     it.Spec.Stage,
			Freight:   it.Spec.Freight,
			Phase:     it.Status.Phase,
			Message:   it.Status.Message,
			StartedAt: it.Status.StartedAt,
			CreatedAt: it.Metadata.CreationTimestamp,
		})
	}
	return out, nil
}

// KargoFreight is a Freight, reduced to what names it to a reader.
type KargoFreight struct {
	Name      string
	Namespace string
	// Alias is Kargo's own human name for the freight -- "mellow-mongoose"
	// rather than "f-7c3d9a1". It is what the UI shows and what somebody who
	// has been looking at this pipeline will recognise, and it lives in a
	// label rather than a field.
	Alias string
	// Artifacts are what the freight carries, each written the way its own
	// ecosystem writes it: `repo:tag` for an image, `chart:version` for a
	// chart, `repo@sha` for a commit. Empty for a freight that carries
	// nothing this reads, which is a real shape and not an error.
	Artifacts []string
}

// Freight reads one Freight by name.
//
// A GET of the one object, not a list. Kargo creates a Freight per discovery
// and never deletes them by default, so a cluster that has been running for a
// year holds thousands, and listing them all to find the two a sweep will
// actually name would be the most expensive read in this package by an order
// of magnitude. The names come from the Stage and the Promotion, which have
// already been read.
func (a *APIServer) Freight(ctx context.Context, namespace, name string) (KargoFreight, error) {
	if namespace == "" || name == "" {
		return KargoFreight{}, fmt.Errorf("no freight reference to read")
	}
	// Images, charts and commits are top-level on a Freight. There is no
	// spec: a Freight is a record of what was found, so it has nothing a user
	// declares and Kargo never adopted the shape.
	var raw struct {
		Metadata struct {
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
		Images []struct {
			RepoURL string `json:"repoURL"`
			Tag     string `json:"tag"`
			Digest  string `json:"digest"`
		} `json:"images"`
		Charts []struct {
			RepoURL string `json:"repoURL"`
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"charts"`
		Commits []struct {
			RepoURL string `json:"repoURL"`
			ID      string `json:"id"`
			Tag     string `json:"tag"`
		} `json:"commits"`
	}
	if err := a.get(ctx, readFreight.namespaced(namespace, name), &raw); err != nil {
		switch code(err) {
		case http.StatusForbidden, http.StatusUnauthorized:
			return KargoFreight{}, fmt.Errorf("not permitted to read freight in %s", namespace)
		case http.StatusNotFound:
			return KargoFreight{}, fmt.Errorf("freight %s/%s no longer exists", namespace, name)
		}
		return KargoFreight{}, err
	}

	f := KargoFreight{Name: name, Namespace: namespace, Alias: raw.Metadata.Labels[aliasLabel]}
	for _, im := range raw.Images {
		switch {
		case im.Tag != "":
			f.Artifacts = append(f.Artifacts, im.RepoURL+":"+im.Tag)
		case im.Digest != "":
			// A digest-only image is what a Warehouse discovers when it is
			// subscribed by digest, and truncating it here would produce a
			// reference nobody can paste anywhere.
			f.Artifacts = append(f.Artifacts, im.RepoURL+"@"+im.Digest)
		}
	}
	for _, ch := range raw.Charts {
		// An OCI chart has no separate name; its repoURL is the whole
		// address, and joining an empty name to a version would print ":1.2.3".
		who := ch.Name
		if who == "" {
			who = ch.RepoURL
		}
		if who != "" && ch.Version != "" {
			f.Artifacts = append(f.Artifacts, who+":"+ch.Version)
		}
	}
	for _, c := range raw.Commits {
		switch {
		case c.Tag != "":
			f.Artifacts = append(f.Artifacts, c.RepoURL+"@"+c.Tag)
		case c.ID != "":
			f.Artifacts = append(f.Artifacts, c.RepoURL+"@"+shortSHA(c.ID))
		}
	}
	return f, nil
}

// aliasLabel is where Kargo keeps the freight's human name.
const aliasLabel = "kargo.akuity.io/alias"

// shortSHA is the seven characters everything else in this ecosystem prints.
func shortSHA(id string) string {
	if len(id) > 7 {
		return id[:7]
	}
	return id
}

type meta struct {
	Name              string    `json:"name"`
	Namespace         string    `json:"namespace"`
	CreationTimestamp time.Time `json:"creationTimestamp"`
}

type condition struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"`
	Reason             string    `json:"reason"`
	Message            string    `json:"message"`
	LastTransitionTime time.Time `json:"lastTransitionTime"`
}

func findCondition(cs []condition, typ string) (condition, bool) {
	for _, c := range cs {
		if c.Type == typ {
			return c, true
		}
	}
	return condition{}, false
}

// listAll walks every page of a cluster-scoped collection list.
//
// Paged for the same reason CountLive is: a fleet with a thousand promotions
// is ordinary, and a supervisor that silently read the first five hundred
// would report a wedged Stage as healthy because its promotion was on page
// two. The pages are appended into the caller's Items slice by re-decoding,
// which costs an allocation and removes a class of bug.
func (a *APIServer) listAll(ctx context.Context, read Read, out any) error {
	plural := read.Plural
	type page struct {
		Items    []json.RawMessage `json:"items"`
		Metadata struct {
			Continue string `json:"continue"`
		} `json:"metadata"`
	}
	var all []json.RawMessage
	cont := ""
	for {
		path := fmt.Sprintf("%s?limit=%d", read.collection(), pageSize)
		if cont != "" {
			path += "&continue=" + url.QueryEscape(cont)
		}
		var p page
		if err := a.get(ctx, path, &p); err != nil {
			switch code(err) {
			case http.StatusForbidden, http.StatusUnauthorized:
				return fmt.Errorf("not permitted to list %s", plural)
			case http.StatusNotFound:
				return fmt.Errorf("this cluster does not serve %s", plural)
			}
			return err
		}
		all = append(all, p.Items...)
		if p.Metadata.Continue == "" || len(all) >= maxItems {
			break
		}
		cont = p.Metadata.Continue
	}
	merged, err := json.Marshal(map[string]any{"items": all})
	if err != nil {
		return err
	}
	return json.Unmarshal(merged, out)
}
