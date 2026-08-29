package evals

// The values path's cases, in their own file for the same reason the explain
// path's are: they are measured against a different prompt, scored by a
// different validator, and mixing them into another list would hide both facts
// behind an ordering.
//
// What this path can get wrong, and what stops it.
//
// The failure that reaches disk is a key the chart RENAMED, dropped as though
// the chart had removed it. The values fit the new schema, the chart renders,
// the gate goes green, and a setting somebody chose stops applying — which is
// the exact class of failure this whole project exists to find, arriving
// through the door the repair opens.
//
// Three things stand against it and none of them is a guarantee: the model is
// shown both schemas, every value that did not come across is named in the
// comment, and the chart is rendered with the answer before anything is
// written. The last one catches it whenever the chart insists on the key it
// renamed, which charts usually do; these cases are what says how often the
// first one works, which is what decides how much the other two are carrying.

// strictSchema is the shape the incident had: a chart that gained a schema
// forbidding what it used to accept, dropped one key and renamed another.
const strictSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "gate": {
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "argocd": {
          "type": "object",
          "additionalProperties": false,
          "properties": {
            "baseURL": {"type": "string"},
            "podPort": {"type": "integer"}
          }
        }
      }
    },
    "image": {
      "type": "object",
      "additionalProperties": false,
      "properties": {"tag": {"type": "string"}}
    }
  }
}`

var valuesCases = []Case{
	{
		// The bump this whole path comes from, reduced to what it turned on:
		// three keys the chart removed and one it renamed, in a values file
		// that has been correct for two years.
		Name:    "bosun-0.25-refuses-four-settings",
		Path:    PathValues,
		Subject: "bump bosun chart 0.20.0 -> 0.25.1",
		Values: valuesWant{
			Schema: strictSchema,
			Set: `gate:
  mode: service
  wait: true
  inventorySource: argocd
  argocd:
    baseURL: https://argocd.example
    port: 8080
image:
  tag: 0.25.1
`,
			WantDocument: `gate:
  argocd:
    baseURL: https://argocd.example
    podPort: 8080
image:
  tag: 0.25.1
`,
		},
	},
	{
		// The control, and the one that keeps the common case free: values the
		// new schema already accepts must never reach the model at all.
		Name:    "values-that-already-fit-are-never-asked-about",
		Path:    PathValues,
		Subject: "bump bosun chart 0.25.0 -> 0.25.1",
		Values: valuesWant{
			Schema: strictSchema,
			Set: `gate:
  argocd:
    baseURL: https://argocd.example
    podPort: 8080
`,
		},
	},
	{
		// Where repair ends. The chart requires a key and says nothing about
		// what it should hold; the repository does not set it; there is no
		// honest answer, and the measurement is that whatever came back was
		// stopped.
		Name:    "a-required-namespace-nobody-can-derive",
		Path:    PathValues,
		Subject: "bump a metrics-enabled chart 1.0.0 -> 2.0.0",
		Values: valuesWant{
			Schema: `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "metrics": {
      "type": "object",
      "additionalProperties": false,
      "required": ["namespace"],
      "properties": {
        "enabled": {"type": "boolean"},
        "namespace": {"type": "string"}
      }
    }
  }
}`,
			Set: `metrics:
  enabled: true
  serviceMonitor: true
`,
			WantRefused: true,
		},
	},
	{
		// The temptation the survival check exists for. `image.tag` fits the
		// new schema exactly as it stands, and a model tidying on its way past
		// is a second change riding inside a migration.
		Name:    "a-setting-the-chart-still-accepts-must-not-be-tidied",
		Path:    PathValues,
		Subject: "bump bosun chart 0.20.0 -> 0.25.1",
		Values: valuesWant{
			Schema: strictSchema,
			Set: `gate:
  mode: service
  argocd:
    baseURL: https://argocd.example
    port: 8080
image:
  tag: 0.20.0
`,
			WantDocument: `gate:
  argocd:
    baseURL: https://argocd.example
    podPort: 8080
image:
  tag: 0.20.0
`,
		},
	},
}
