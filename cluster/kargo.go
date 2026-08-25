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
// Same three rules as the rest of this package: only GET and LIST, never an
// error for "could not look", and no vendored types. The last one earns its
// keep here more than anywhere else -- these structs name eleven fields out of
// Kargo's CRDs, and a release that adds a field to any of them cannot break
// this build.

const kargoAPI = "/apis/kargo.akuity.io/v1alpha1"

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
	return a.get(ctx, kargoAPI, nil) == nil
}

// Stages lists every Stage the agent may see.
//
// The `yaml-update` steps are the interesting part and the reason this reads
// the promotion template at all: they are the authoritative list of which
// files and keys promotions actually rewrite. Reading the same information
// out of the repository's Kargo values would answer a different question --
// what the target list SAYS -- and the two diverge exactly when something is
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
				} `json:"freightHistory"`
				Conditions []condition `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := a.listAll(ctx, "stages", &raw); err != nil {
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
	if err := a.listAll(ctx, "warehouses", &raw); err != nil {
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
	if err := a.listAll(ctx, "promotions", &raw); err != nil {
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
func (a *APIServer) listAll(ctx context.Context, plural string, out any) error {
	type page struct {
		Items    []json.RawMessage `json:"items"`
		Metadata struct {
			Continue string `json:"continue"`
		} `json:"metadata"`
	}
	var all []json.RawMessage
	cont := ""
	for {
		path := fmt.Sprintf("%s/%s?limit=%d", kargoAPI, plural, pageSize)
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
