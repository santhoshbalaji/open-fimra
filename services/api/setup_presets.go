package main

// Hierarchy presets for the first-run wizard (PLAN.md section 5.2). The top
// level is always the state, whose boundary becomes the workspace boundary.
// Labels can be edited in the wizard.

type levelLabels map[string]string

type presetLevel struct {
	Slug   string      `json:"slug"`
	Labels levelLabels `json:"labels"`
}

type hierarchyPreset struct {
	Slug   string        `json:"slug"`
	Name   string        `json:"name"`
	Levels []presetLevel `json:"levels"`
}

var (
	stateLevel       = presetLevel{"state", levelLabels{"en": "State", "hi": "राज्य"}}
	circleLevel      = presetLevel{"circle", levelLabels{"en": "Circle", "hi": "वृत्त"}}
	divisionLevel    = presetLevel{"division", levelLabels{"en": "Division", "hi": "प्रभाग"}}
	subDivisionLevel = presetLevel{"sub_division", levelLabels{"en": "Sub-division", "hi": "उप-प्रभाग"}}
	rangeLevel       = presetLevel{"range", levelLabels{"en": "Range", "hi": "रेंज"}}
	sectionLevel     = presetLevel{"section", levelLabels{"en": "Section", "hi": "अनुभाग"}}
	beatLevel        = presetLevel{"beat", levelLabels{"en": "Beat", "hi": "बीट"}}
	compartmentLevel = presetLevel{"compartment", levelLabels{"en": "Compartment", "hi": "कक्ष"}}
)

var hierarchyPresets = []hierarchyPreset{
	{"circle-division-range-beat", "State → Circle → Division → Range → Beat",
		[]presetLevel{stateLevel, circleLevel, divisionLevel, rangeLevel, beatLevel}},
	{"circle-division-subdivision-range-section-beat", "State → Circle → Division → Sub-division → Range → Section → Beat",
		[]presetLevel{stateLevel, circleLevel, divisionLevel, subDivisionLevel, rangeLevel, sectionLevel, beatLevel}},
	{"circle-division-range-section-beat", "State → Circle → Division → Range → Section → Beat",
		[]presetLevel{stateLevel, circleLevel, divisionLevel, rangeLevel, sectionLevel, beatLevel}},
	{"circle-division-range-beat-compartment", "State → Circle → Division → Range → Beat → Compartment",
		[]presetLevel{stateLevel, circleLevel, divisionLevel, rangeLevel, beatLevel, compartmentLevel}},
}

func findPreset(slug string) (hierarchyPreset, bool) {
	for _, preset := range hierarchyPresets {
		if preset.Slug == slug {
			return preset, true
		}
	}
	return hierarchyPreset{}, false
}
