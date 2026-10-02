package agent

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	agentv1 "github.com/depot/cli/pkg/proto/depot/agent/v1"
)

// Plugin views follow the core block vocabulary v1 (DEP-7248).
const (
	viewLineCap    = 40
	maxViewKeyLen  = 8
	maxChipValue   = 24
	statusBarWidth = 10
	maxBlockDepth  = 2
)

func cliViewClient() *agentv1.ViewClient {
	return &agentv1.ViewClient{Name: "cli", Blocks: 1, ViewKinds: []string{"cli"}}
}

type pluginViews struct {
	Rev   int64        `json:"rev"`
	Views []pluginView `json:"views"`
}

type pluginView struct {
	Plugin string    `json:"plugin"`
	ID     string    `json:"id"`
	Rev    int64     `json:"rev"`
	Title  string    `json:"title"`
	Blocks blockList `json:"blocks"`
	Kinds  struct {
		CLI *cliKind `json:"cli"`
	} `json:"kinds"`
}

type cliKind struct {
	Keys    []cliKey `json:"keys"`
	Compact string   `json:"compact"`
}

type cliKey struct {
	Key    string  `json:"key"`
	Action string  `json:"action"`
	Value  *string `json:"value"`
}

type viewBlock struct {
	Type      string       `json:"type"`
	Alt       string       `json:"alt"`
	Title     string       `json:"title"`
	Text      string       `json:"text"`
	Tone      string       `json:"tone"`
	Muted     bool         `json:"muted"`
	Blocks    blockList    `json:"blocks"`
	Accessory *viewElement `json:"accessory"`
	Collapsed bool         `json:"collapsed"`
	Items     []blockItem  `json:"items"`
	Ordered   bool         `json:"ordered"`
	Columns   []struct {
		Label string `json:"label"`
		Align string `json:"align"`
	} `json:"columns"`
	Rows     [][]string    `json:"rows"`
	Label    string        `json:"label"`
	Progress *float64      `json:"progress"`
	Detail   string        `json:"detail"`
	URL      string        `json:"url"`
	Elements []viewElement `json:"elements"`
	ID       string        `json:"id"`
	Inputs   []formInput   `json:"inputs"`
	Submit   *viewElement  `json:"submit"`
	// invalid marks a block that failed to decode, so it renders like an unknown type.
	invalid bool
}

// blockItem covers both list items and fields items.
type blockItem struct {
	ID        string       `json:"id"`
	Text      string       `json:"text"`
	Checked   *bool        `json:"checked"`
	Tone      string       `json:"tone"`
	Accessory *viewElement `json:"accessory"`
	Label     string       `json:"label"`
	Value     string       `json:"value"`
}

type viewElement struct {
	Type        string         `json:"type"`
	Label       string         `json:"label"`
	Action      string         `json:"action"`
	Value       *string        `json:"value"`
	Style       string         `json:"style"`
	Confirm     *viewConfirm   `json:"confirm"`
	Placeholder string         `json:"placeholder"`
	Options     []selectOption `json:"options"`
}

type viewConfirm struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

type selectOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type formInput struct {
	Name      string         `json:"name"`
	Label     string         `json:"label"`
	Kind      string         `json:"kind"`
	Options   []selectOption `json:"options"`
	Required  bool           `json:"required"`
	MaxLength int            `json:"maxLength"`
}

// blockList decodes each block on its own, so one malformed block shows as "not shown"
// instead of hiding the whole view.
type blockList []viewBlock

func (l *blockList) UnmarshalJSON(b []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	out := make(blockList, len(raw))
	for i, r := range raw {
		if err := json.Unmarshal(r, &out[i]); err != nil {
			var alt struct {
				Alt string `json:"alt"`
			}
			_ = json.Unmarshal(r, &alt)
			out[i] = viewBlock{invalid: true, Alt: alt.Alt}
		}
	}
	*l = out
	return nil
}

func parsePluginViews(s string) (pluginViews, error) {
	var v pluginViews
	if s == "" {
		return v, nil
	}
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return v, fmt.Errorf("decode plugin views: %w", err)
	}
	return v, nil
}

func (v pluginView) ref() string { return v.Plugin + "/" + v.ID }

// chip is one invocable control of a view, addressed by its key.
type chip struct {
	Key     string
	Action  string
	Value   *string
	Kind    string // "button", "select", "form" or "key"
	Label   string
	Style   string
	Confirm *viewConfirm
	Options []selectOption
	Inputs  []formInput
}

func (c chip) needsConfirm() bool { return c.Confirm != nil || c.Style == "danger" }

type seg struct {
	text  string
	style lipgloss.Style
}

type viewLine []seg

var (
	plainStyle = lipgloss.NewStyle()
	boldStyle  = lipgloss.NewStyle().Bold(true)
	mutedStyle = lipgloss.NewStyle().Faint(true)
	toneColors = map[string]string{"info": "4", "success": "2", "warning": "3", "danger": "1"}
)

func toneStyle(tone string, muted bool) lipgloss.Style {
	st := lipgloss.NewStyle()
	if c, ok := toneColors[tone]; ok {
		st = st.Foreground(lipgloss.Color(c))
	}
	if muted {
		st = st.Faint(true)
	}
	return st
}

// span sanitises s and flattens it to one line; every plugin string passes through it before any style.
func span(s string, st lipgloss.Style) seg {
	s = strings.ReplaceAll(safeText(s), "\t", "    ")
	return seg{text: strings.ReplaceAll(s, "\n", " "), style: st}
}

// lines splits plugin text on newlines, sanitising each line.
func lines(s string) []string {
	return strings.Split(strings.ReplaceAll(safeText(s), "\r", ""), "\n")
}

// viewRenderer lays out one view; chips collect its controls in display order.
type viewRenderer struct {
	width int
	out   []viewLine
	chips []chip
	used  map[string]bool
	keys  []cliKey
	taken []bool
}

func layoutView(v pluginView, width int) ([]viewLine, []chip) {
	vr := &viewRenderer{width: max(width, 20), used: map[string]bool{}}
	if v.Kinds.CLI != nil {
		vr.keys = v.Kinds.CLI.Keys
		vr.taken = make([]bool, len(vr.keys))
	}
	vr.blocks(v.Blocks, 0, "")
	var extra viewLine
	for i, k := range vr.keys {
		if vr.taken[i] {
			continue
		}
		key := vr.chipKey(&k, i)
		vr.chips = append(vr.chips, chip{Key: key, Action: k.Action, Value: k.Value, Kind: "key", Label: k.Action})
		if len(extra) > 0 {
			extra = append(extra, span("  ", plainStyle))
		}
		extra = append(extra, vr.chipSegs(vr.chips[len(vr.chips)-1])...)
	}
	if len(extra) > 0 {
		vr.out = append(vr.out, extra)
	}
	return vr.out, vr.chips
}

func viewRule(v pluginView, width int, compact string) string {
	l := viewLine{span("── "+v.Plugin+": "+v.Title+" ──", boldStyle)}
	if compact != "" {
		l = append(l, span(" "+compact, plainStyle))
	}
	return renderLine(l, width)
}

// renderView returns a view's printable lines, capped, plus its chips.
func renderView(v pluginView, width int, compact bool) ([]string, []chip) {
	body, chips := layoutView(v, width)
	if compact && v.Kinds.CLI != nil && strings.TrimSpace(v.Kinds.CLI.Compact) != "" {
		return []string{viewRule(v, width, v.Kinds.CLI.Compact)}, chips
	}
	out := []string{viewRule(v, width, "")}
	for i, l := range body {
		if i == viewLineCap {
			out = append(out, renderLine(viewLine{span(fmt.Sprintf("… %d more lines", len(body)-viewLineCap), mutedStyle)}, width))
			break
		}
		out = append(out, renderLine(l, width))
	}
	return out, chips
}

// renderLine clips a line to width by its sanitised text, then styles it.
func renderLine(l viewLine, width int) string {
	var b strings.Builder
	left := width
	for _, s := range l {
		if left <= 0 {
			break
		}
		t := s.text
		if w := ansi.StringWidth(t); w > left {
			t = ansi.Truncate(t, left, "…")
		}
		left -= ansi.StringWidth(t)
		if t != "" {
			b.WriteString(s.style.Render(t))
		}
	}
	return b.String()
}

func (vr *viewRenderer) add(indent string, segs ...seg) {
	if indent != "" {
		segs = append(viewLine{{text: indent, style: plainStyle}}, segs...)
	}
	vr.out = append(vr.out, segs)
}

func (vr *viewRenderer) blocks(bs []viewBlock, depth int, indent string) {
	for _, b := range bs {
		vr.block(b, depth, indent)
	}
}

func (vr *viewRenderer) block(b viewBlock, depth int, indent string) {
	if b.invalid {
		vr.unknown(b, indent)
		return
	}
	switch b.Type {
	case "section":
		title := viewLine{}
		if b.Collapsed {
			title = append(title, span("▸ ", mutedStyle))
		}
		if b.Title != "" {
			title = append(title, span(b.Title, boldStyle))
		}
		if b.Accessory != nil {
			title = append(title, span("  ", plainStyle))
			title = append(title, vr.element(*b.Accessory)...)
		}
		if len(title) > 0 {
			vr.add(indent, title...)
		}
		if b.Collapsed {
			return
		}
		if b.Text != "" {
			for _, l := range lines(b.Text) {
				vr.add(indent, span(l, plainStyle))
			}
		}
		if depth+1 >= maxBlockDepth {
			for range b.Blocks {
				vr.add(indent+"  ", span("1 item not shown", mutedStyle))
			}
			return
		}
		vr.blocks(b.Blocks, depth+1, indent+"  ")
	case "text":
		for _, l := range lines(b.Text) {
			vr.add(indent, span(l, toneStyle(b.Tone, b.Muted)))
		}
	case "markdown":
		for _, l := range lines(markdownLinks(b.Text)) {
			vr.add(indent, span(l, plainStyle))
		}
	case "fields":
		for _, it := range b.Items {
			vr.add(indent, span(it.Label+": ", mutedStyle), span(it.Value, plainStyle))
		}
	case "list":
		for i, it := range b.Items {
			mark := "• "
			switch {
			case it.Checked != nil && *it.Checked:
				mark = "[x] "
			case it.Checked != nil:
				mark = "[ ] "
			case b.Ordered:
				mark = strconv.Itoa(i+1) + ". "
			}
			l := viewLine{span(mark, plainStyle), span(it.Text, toneStyle(it.Tone, it.Checked != nil && *it.Checked))}
			if it.Accessory != nil {
				l = append(l, span("  ", plainStyle))
				l = append(l, vr.element(*it.Accessory)...)
			}
			vr.add(indent, l...)
		}
	case "table":
		vr.table(b, indent)
	case "status":
		l := viewLine{span("● ", toneStyle(b.Tone, false)), span(b.Label, plainStyle)}
		if b.Progress != nil {
			p := min(max(*b.Progress, 0), 1)
			filled := int(p*statusBarWidth + 0.5)
			bar := "▕" + strings.Repeat("█", filled) + strings.Repeat("·", statusBarWidth-filled) + "▏"
			l = append(l, span("  "+bar+" ", toneStyle(b.Tone, false)), span(fmt.Sprintf("%d%%", int(p*100+0.5)), plainStyle))
		}
		if b.Detail != "" {
			l = append(l, span("  "+b.Detail, mutedStyle))
		}
		vr.add(indent, l...)
	case "image":
		vr.add(indent, span("[image: "+b.Alt+"]", mutedStyle))
	case "link":
		vr.add(indent, linkSegs(b.Text, b.URL)...)
	case "divider":
		vr.add(indent, span(strings.Repeat("─", max(vr.width-len(indent), 1)), mutedStyle))
	case "actions":
		vr.chipRow(indent, b.Elements)
	case "form":
		if b.Submit == nil {
			vr.unknown(b, indent)
			return
		}
		c := vr.newChip(*b.Submit)
		c.Kind = "form"
		c.Inputs = b.Inputs
		vr.chips = append(vr.chips, c)
		var names []string
		for _, in := range b.Inputs {
			n := in.Label
			if n == "" {
				n = in.Name
			}
			if in.Required {
				n += "*"
			}
			names = append(names, n)
		}
		l := vr.chipSegs(c)
		if len(names) > 0 {
			l = append(l, span(" ("+strings.Join(names, ", ")+")", mutedStyle))
		}
		vr.add(indent, l...)
	default:
		vr.unknown(b, indent)
	}
}

func (vr *viewRenderer) unknown(b viewBlock, indent string) {
	if strings.TrimSpace(b.Alt) != "" {
		for _, l := range lines(b.Alt) {
			vr.add(indent, span(l, plainStyle))
		}
		return
	}
	vr.add(indent, span("1 item not shown", mutedStyle))
}

func linkSegs(text, raw string) viewLine {
	if text == "" {
		text = raw
	}
	if !httpsURL(raw) {
		return viewLine{span(text, plainStyle)}
	}
	return viewLine{span(text, lipgloss.NewStyle().Underline(true)), span(" ("+raw+")", mutedStyle)}
}

func httpsURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && safeText(raw) == raw
}

var markdownLink = regexp.MustCompile(`\[([^\]\n]*)\]\(([^)\s]*)\)`)

// markdownLinks spells out each link's target, so the real destination is visible.
func markdownLinks(s string) string {
	return markdownLink.ReplaceAllStringFunc(s, func(m string) string {
		parts := markdownLink.FindStringSubmatch(m)
		if !httpsURL(parts[2]) {
			return parts[1]
		}
		return parts[1] + " (" + parts[2] + ")"
	})
}

func (vr *viewRenderer) table(b viewBlock, indent string) {
	cols := len(b.Columns)
	for _, r := range b.Rows {
		cols = max(cols, len(r))
	}
	cols = min(cols, 8)
	if cols == 0 {
		return
	}
	rows := b.Rows
	if len(rows) > 100 {
		rows = rows[:100]
	}
	cell := func(r []string, i int) string {
		if i < len(r) {
			return span(r[i], plainStyle).text
		}
		return ""
	}
	header := make([]string, cols)
	widths := make([]int, cols)
	for i := range header {
		if i < len(b.Columns) {
			header[i] = span(b.Columns[i].Label, plainStyle).text
		}
		widths[i] = ansi.StringWidth(header[i])
		for _, r := range rows {
			widths[i] = max(widths[i], ansi.StringWidth(cell(r, i)))
		}
	}
	avail := vr.width - len(indent) - 2*(cols-1)
	for total(widths) > avail {
		w := 0
		for i := range widths {
			if widths[i] > widths[w] {
				w = i
			}
		}
		if widths[w] <= 1 {
			break
		}
		widths[w]--
	}
	format := func(r []string, st lipgloss.Style) viewLine {
		var l viewLine
		for i := 0; i < cols; i++ {
			t := r[i]
			if ansi.StringWidth(t) > widths[i] {
				t = ansi.Truncate(t, widths[i], "…")
			}
			pad := strings.Repeat(" ", widths[i]-ansi.StringWidth(t))
			if i < len(b.Columns) && b.Columns[i].Align == "right" {
				t = pad + t
			} else if i < cols-1 {
				t += pad
			}
			if i > 0 {
				l = append(l, seg{text: "  ", style: plainStyle})
			}
			l = append(l, seg{text: t, style: st})
		}
		return l
	}
	if len(b.Columns) > 0 {
		vr.add(indent, format(header, boldStyle)...)
	}
	for _, r := range rows {
		cells := make([]string, cols)
		for i := range cells {
			cells[i] = cell(r, i)
		}
		vr.add(indent, format(cells, plainStyle)...)
	}
}

func total(ws []int) int {
	n := 0
	for _, w := range ws {
		n += w
	}
	return n
}

func (vr *viewRenderer) chipRow(indent string, els []viewElement) {
	var l viewLine
	width := len(indent)
	for _, el := range els {
		segs := vr.element(el)
		w := 0
		for _, s := range segs {
			w += ansi.StringWidth(s.text)
		}
		if len(l) > 0 && width+2+w > vr.width {
			vr.add(indent, l...)
			l, width = nil, len(indent)
		}
		if len(l) > 0 {
			l = append(l, span("  ", plainStyle))
			width += 2
		}
		l = append(l, segs...)
		width += w
	}
	if len(l) > 0 {
		vr.add(indent, l...)
	}
}

// element registers a control as a chip and returns its printed form.
func (vr *viewRenderer) element(el viewElement) viewLine {
	c := vr.newChip(el)
	vr.chips = append(vr.chips, c)
	return vr.chipSegs(c)
}

func (vr *viewRenderer) newChip(el viewElement) chip {
	c := chip{Action: el.Action, Value: el.Value, Kind: el.Type, Label: el.Label, Style: el.Style, Confirm: el.Confirm, Options: el.Options}
	if el.Type == "select" {
		c.Label, c.Value = el.Placeholder, nil
	}
	if c.Label == "" {
		c.Label = el.Action
	}
	c.Key = vr.matchKey(c)
	return c
}

// matchKey takes the first unused kinds.cli.keys entry for this control, else the next number.
func (vr *viewRenderer) matchKey(c chip) string {
	for i := range vr.keys {
		k := &vr.keys[i]
		if vr.taken[i] || k.Action != c.Action {
			continue
		}
		if k.Value != nil && !chipTakesValue(c, *k.Value) {
			continue
		}
		vr.taken[i] = true
		return vr.chipKey(k, i)
	}
	return vr.nextNumber()
}

func chipTakesValue(c chip, v string) bool {
	if c.Kind == "select" {
		for _, o := range c.Options {
			if o.Value == v {
				return true
			}
		}
		return false
	}
	return c.Value != nil && *c.Value == v
}

func (vr *viewRenderer) chipKey(k *cliKey, i int) string {
	vr.taken[i] = true
	key := k.Key
	if validViewKey(key) && !vr.used[key] {
		vr.used[key] = true
		return key
	}
	return vr.nextNumber()
}

func (vr *viewRenderer) nextNumber() string {
	for n := 1; ; n++ {
		if k := strconv.Itoa(n); !vr.used[k] {
			vr.used[k] = true
			return k
		}
	}
}

// validViewKey accepts short printable keys that cannot be mistaken for a chip number.
func validViewKey(k string) bool {
	if k == "" || len([]rune(k)) > maxViewKeyLen || safeText(k) != k {
		return false
	}
	digits := true
	for _, r := range k {
		if unicode.IsSpace(r) || !unicode.IsPrint(r) {
			return false
		}
		digits = digits && unicode.IsDigit(r)
	}
	return !digits
}

func (vr *viewRenderer) chipSegs(c chip) viewLine {
	st := plainStyle
	switch c.Style {
	case "danger":
		st = toneStyle("danger", false)
	case "primary":
		st = boldStyle
	}
	l := viewLine{span("["+c.Key+"] ", boldStyle), span(c.Label, st)}
	switch {
	case c.Kind == "select":
		var opts []string
		for _, o := range c.Options {
			opts = append(opts, o.Value)
		}
		l = append(l, span(" ▾ ("+strings.Join(opts, "|")+")", mutedStyle))
	case c.Value != nil && *c.Value != "":
		v := []rune(span(*c.Value, plainStyle).text)
		if len(v) > maxChipValue {
			v = append(v[:maxChipValue-1], '…')
		}
		l = append(l, span(" ("+string(v)+")", mutedStyle))
	}
	return l
}
