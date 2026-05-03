package tui

import (
	"fmt"
	"image"
	"image/color"
	"math/rand/v2"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// MinigameDialog is the modal opened with ctrl+g. On open it picks one game
// at random from the registry below — ctrl+n re-rolls. Each game implements
// the `minigame` interface; the dialog only owns the title bar, the
// shared footer hint, and the random pick.
type MinigameDialog struct {
	styles Styles
	game   minigame
}

// minigame is the per-game contract. Init kicks off whatever ticking the
// game needs; Update consumes both keystrokes and the game's own tick
// messages; Render returns the inner content (excluding outer dialog
// chrome) given the available inner width.
type minigame interface {
	Name() string
	Init() tea.Cmd
	Update(msg tea.Msg) tea.Cmd
	Render(s Styles, innerW int) string
}

// gameBuilders is the random pool. Adding a new game = appending one
// constructor here.
var gameBuilders = []func() minigame{
	func() minigame { return newSnakeMinigame() },
	func() minigame { return new2048Minigame() },
	func() minigame { return newTetrisMinigame() },
}

func NewMinigameDialog(s Styles) *MinigameDialog {
	pick := gameBuilders[rand.IntN(len(gameBuilders))]
	return &MinigameDialog{styles: s, game: pick()}
}

func (d *MinigameDialog) SetStyles(s Styles) { d.styles = s }

func (d *MinigameDialog) Init() tea.Cmd { return d.game.Init() }

func (d *MinigameDialog) Update(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc", "ctrl+c", "ctrl+g":
			return func() tea.Msg { return CloseDialogMsg{} }
		case "ctrl+n":
			// Re-roll: pick a fresh random game. Old in-flight ticks land
			// on the dead game's instance ID and are dropped.
			pick := gameBuilders[rand.IntN(len(gameBuilders))]
			d.game = pick()
			return d.game.Init()
		}
	}
	return d.game.Update(msg)
}

func (d *MinigameDialog) View(width, _ int) string {
	boxW := 66
	if boxW > width-4 {
		boxW = width - 4
	}
	if boxW < 40 {
		boxW = 40
	}
	innerW := boxW - 6

	title := HatchedTitle(d.game.Name(), innerW, colPrimary, colAccent, d.styles.DialogTitle)
	body := d.game.Render(d.styles, innerW)
	hint := d.styles.Hint.Render("ctrl+n nuevo juego · esc cerrar")

	return d.styles.DialogBox.Width(boxW).Render(strings.Join([]string{
		title, "", body, "", hint,
	}, "\n"))
}

// =============================================================================
// snake
// =============================================================================

const (
	snakeGridW    = 30
	snakeGridH    = 15
	snakeTickMS   = 110
	snakeCellChar = "██"
)

type snakeTickMsg struct{ instance int }

var snakeInstanceSeq int

type snakeMini struct {
	w, h     int
	body     []image.Point
	dir      image.Point
	queued   image.Point
	food     image.Point
	over     bool
	won      bool
	paused   bool
	growing  int
	instance int
}

func newSnakeMinigame() *snakeMini {
	snakeInstanceSeq++
	g := &snakeMini{w: snakeGridW, h: snakeGridH, instance: snakeInstanceSeq}
	g.reset()
	return g
}

func (s *snakeMini) reset() {
	cx, cy := s.w/2, s.h/2
	s.body = []image.Point{
		{X: cx, Y: cy},
		{X: cx - 1, Y: cy},
		{X: cx - 2, Y: cy},
	}
	s.dir = image.Pt(1, 0)
	s.queued = image.Pt(1, 0)
	s.over = false
	s.won = false
	s.paused = false
	s.growing = 0
	s.placeFood()
}

func (s *snakeMini) Name() string { return "snake" }

func (s *snakeMini) Init() tea.Cmd { return s.tick() }

func (s *snakeMini) tick() tea.Cmd {
	id := s.instance
	return tea.Tick(snakeTickMS*time.Millisecond, func(time.Time) tea.Msg {
		return snakeTickMsg{instance: id}
	})
}

func (s *snakeMini) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case snakeTickMsg:
		if m.instance != s.instance {
			return nil
		}
		s.step()
		return s.tick()
	case tea.KeyMsg:
		switch m.String() {
		case "up", "k", "w":
			s.queueDir(0, -1)
		case "down", "j", "s":
			s.queueDir(0, 1)
		case "left", "h", "a":
			s.queueDir(-1, 0)
		case "right", "l", "d":
			s.queueDir(1, 0)
		case "enter", " ":
			if s.over || s.won {
				s.reset()
			}
		case "p":
			s.paused = !s.paused
		}
	}
	return nil
}

func (s *snakeMini) score() int { return len(s.body) - 3 }

// queueDir stages a direction change for the next tick. Reversing 180° onto
// your own neck is rejected — without that guard, rapid up→left presses
// inside one tick would loop the snake into itself instantly.
func (s *snakeMini) queueDir(dx, dy int) {
	if s.over || s.won || s.paused {
		return
	}
	if dx == -s.dir.X && dy == -s.dir.Y {
		return
	}
	s.queued = image.Pt(dx, dy)
}

func (s *snakeMini) step() {
	if s.over || s.won || s.paused {
		return
	}
	s.dir = s.queued
	head := s.body[0]
	next := image.Pt(head.X+s.dir.X, head.Y+s.dir.Y)
	if next.X < 0 || next.X >= s.w || next.Y < 0 || next.Y >= s.h {
		s.over = true
		return
	}
	limit := len(s.body)
	if s.growing == 0 {
		limit-- // tail moves out this same tick
	}
	for i := 0; i < limit; i++ {
		if s.body[i] == next {
			s.over = true
			return
		}
	}
	s.body = append([]image.Point{next}, s.body...)
	if next == s.food {
		s.growing++
		if len(s.body) >= s.w*s.h {
			s.won = true
			return
		}
		s.placeFood()
	}
	if s.growing > 0 {
		s.growing--
	} else {
		s.body = s.body[:len(s.body)-1]
	}
}

func (s *snakeMini) placeFood() {
	occupied := map[image.Point]bool{}
	for _, p := range s.body {
		occupied[p] = true
	}
	free := s.w*s.h - len(occupied)
	if free <= 0 {
		return
	}
	pick := rand.IntN(free)
	for y := 0; y < s.h; y++ {
		for x := 0; x < s.w; x++ {
			p := image.Pt(x, y)
			if occupied[p] {
				continue
			}
			if pick == 0 {
				s.food = p
				return
			}
			pick--
		}
	}
}

func (s *snakeMini) Render(styles Styles, innerW int) string {
	header := styles.HeaderTitle.Render(fmt.Sprintf("score %d", s.score()))
	state := ""
	switch {
	case s.over:
		state = styles.ResultError.Render("game over · enter para reiniciar")
	case s.won:
		state = styles.ResultOK.Render("¡ganaste! · enter para reiniciar")
	case s.paused:
		state = styles.DialogWarning.Render("pausado")
	}
	if state != "" {
		header += "   " + state
	}
	hint := styles.Hint.Render("←↑→↓ / hjkl · p pausa · enter reiniciar")
	grid := lipgloss.PlaceHorizontal(innerW, lipgloss.Center, s.renderGrid())
	return strings.Join([]string{header, "", grid, "", hint}, "\n")
}

func (s *snakeMini) renderGrid() string {
	body := lipgloss.NewStyle().Foreground(colPrimary).Bold(true)
	head := lipgloss.NewStyle().Foreground(colSecondary).Bold(true)
	food := lipgloss.NewStyle().Foreground(colWarning).Bold(true)
	empty := lipgloss.NewStyle().Foreground(colBorder)

	bodySet := map[image.Point]bool{}
	for _, p := range s.body[1:] {
		bodySet[p] = true
	}
	headPt := s.body[0]

	var rows []string
	for y := 0; y < s.h; y++ {
		var line strings.Builder
		for x := 0; x < s.w; x++ {
			p := image.Pt(x, y)
			switch {
			case p == headPt:
				line.WriteString(head.Render(snakeCellChar))
			case bodySet[p]:
				line.WriteString(body.Render(snakeCellChar))
			case p == s.food:
				line.WriteString(food.Render("●●"))
			default:
				line.WriteString(empty.Render("· "))
			}
		}
		rows = append(rows, line.String())
	}
	frame := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(colBorder)
	return frame.Render(strings.Join(rows, "\n"))
}

// =============================================================================
// 2048
// =============================================================================

const g2048Size = 4

type g2048 struct {
	grid  [g2048Size][g2048Size]int
	score int
	over  bool
	won   bool
}

func new2048Minigame() *g2048 {
	g := &g2048{}
	g.spawn()
	g.spawn()
	return g
}

func (g *g2048) Name() string     { return "2048" }
func (g *g2048) Init() tea.Cmd    { return nil }
func (g *g2048) reset() {
	*g = g2048{}
	g.spawn()
	g.spawn()
}

func (g *g2048) Update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	if g.over || g.won {
		if k.String() == "enter" || k.String() == " " {
			g.reset()
		}
		return nil
	}
	moved := false
	switch k.String() {
	case "up", "k", "w":
		moved = g.move(0, -1)
	case "down", "j", "s":
		moved = g.move(0, 1)
	case "left", "h", "a":
		moved = g.move(-1, 0)
	case "right", "l", "d":
		moved = g.move(1, 0)
	}
	if moved {
		g.spawn()
		if g.hasValue(2048) {
			g.won = true
		} else if !g.canMove() {
			g.over = true
		}
	}
	return nil
}

func (g *g2048) move(dx, dy int) bool {
	before := g.grid
	if dy == 0 {
		for r := 0; r < g2048Size; r++ {
			line := []int{g.grid[r][0], g.grid[r][1], g.grid[r][2], g.grid[r][3]}
			if dx > 0 {
				reverseInts(line)
			}
			collapsed, gained := collapseLeft(line)
			g.score += gained
			if dx > 0 {
				reverseInts(collapsed)
			}
			for c := 0; c < g2048Size; c++ {
				g.grid[r][c] = collapsed[c]
			}
		}
	} else {
		for c := 0; c < g2048Size; c++ {
			line := []int{g.grid[0][c], g.grid[1][c], g.grid[2][c], g.grid[3][c]}
			if dy > 0 {
				reverseInts(line)
			}
			collapsed, gained := collapseLeft(line)
			g.score += gained
			if dy > 0 {
				reverseInts(collapsed)
			}
			for r := 0; r < g2048Size; r++ {
				g.grid[r][c] = collapsed[r]
			}
		}
	}
	return before != g.grid
}

// collapseLeft slides non-zero values toward index 0, merging adjacent
// equals once. Each cell can only participate in one merge per move,
// hence canMerge.
func collapseLeft(line []int) ([]int, int) {
	out := make([]int, len(line))
	pos := 0
	score := 0
	canMerge := false
	for _, v := range line {
		if v == 0 {
			continue
		}
		if canMerge && pos > 0 && out[pos-1] == v {
			out[pos-1] = v * 2
			score += v * 2
			canMerge = false
		} else {
			out[pos] = v
			pos++
			canMerge = true
		}
	}
	return out, score
}

func reverseInts(s []int) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

func (g *g2048) spawn() {
	var empties []image.Point
	for r := 0; r < g2048Size; r++ {
		for c := 0; c < g2048Size; c++ {
			if g.grid[r][c] == 0 {
				empties = append(empties, image.Pt(c, r))
			}
		}
	}
	if len(empties) == 0 {
		return
	}
	p := empties[rand.IntN(len(empties))]
	val := 2
	if rand.IntN(10) == 0 {
		val = 4
	}
	g.grid[p.Y][p.X] = val
}

func (g *g2048) hasValue(n int) bool {
	for r := 0; r < g2048Size; r++ {
		for c := 0; c < g2048Size; c++ {
			if g.grid[r][c] == n {
				return true
			}
		}
	}
	return false
}

func (g *g2048) canMove() bool {
	for r := 0; r < g2048Size; r++ {
		for c := 0; c < g2048Size; c++ {
			if g.grid[r][c] == 0 {
				return true
			}
			if c+1 < g2048Size && g.grid[r][c] == g.grid[r][c+1] {
				return true
			}
			if r+1 < g2048Size && g.grid[r][c] == g.grid[r+1][c] {
				return true
			}
		}
	}
	return false
}

func (g *g2048) Render(styles Styles, innerW int) string {
	header := styles.HeaderTitle.Render(fmt.Sprintf("score %d", g.score))
	state := ""
	switch {
	case g.won:
		state = styles.ResultOK.Render("¡2048! · enter para reiniciar")
	case g.over:
		state = styles.ResultError.Render("game over · enter para reiniciar")
	}
	if state != "" {
		header += "   " + state
	}

	cellW := 6
	cellStyle := func(v int) lipgloss.Style {
		base := lipgloss.NewStyle().Width(cellW).Align(lipgloss.Center).Bold(true)
		switch {
		case v == 0:
			return base.Foreground(colBorder)
		case v <= 4:
			return base.Foreground(colMuted)
		case v <= 16:
			return base.Foreground(colTertiary)
		case v <= 64:
			return base.Foreground(colPrimary)
		case v <= 256:
			return base.Foreground(colSecondary)
		case v <= 1024:
			return base.Foreground(colWarning)
		default:
			return base.Foreground(colDanger)
		}
	}

	var rows []string
	for r := 0; r < g2048Size; r++ {
		cells := make([]string, g2048Size)
		for c := 0; c < g2048Size; c++ {
			v := g.grid[r][c]
			label := "·"
			if v != 0 {
				label = fmt.Sprintf("%d", v)
			}
			cells[c] = cellStyle(v).Render(label)
		}
		rows = append(rows, strings.Join(cells, " "))
	}
	gridStr := strings.Join(rows, "\n\n")
	gridStr = lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(colBorder).
		Padding(1, 2).
		Render(gridStr)
	gridStr = lipgloss.PlaceHorizontal(innerW, lipgloss.Center, gridStr)

	hint := styles.Hint.Render("←↑→↓ / hjkl deslizar · enter reiniciar")
	return strings.Join([]string{header, "", gridStr, "", hint}, "\n")
}

// =============================================================================
// tetris
// =============================================================================

const (
	tetrisW       = 10
	tetrisH       = 18
	tetrisTickMS  = 500
)

type tetrisTickMsg struct{ instance int }

var tetrisInstanceSeq int

type tetris struct {
	grid     [tetrisH][tetrisW]int
	pieces   []tetromino
	cur      tetrominoPiece
	score    int
	lines    int
	over     bool
	paused   bool
	instance int
}

type tetromino struct {
	shape [][]bool
	color color.Color
}

type tetrominoPiece struct {
	kind  int
	shape [][]bool
	pos   image.Point
}

func newTetrisMinigame() *tetris {
	tetrisInstanceSeq++
	t := &tetris{
		pieces:   buildTetrominoes(),
		instance: tetrisInstanceSeq,
	}
	t.spawn()
	return t
}

func (t *tetris) Name() string  { return "tetris" }
func (t *tetris) Init() tea.Cmd { return t.tick() }

func (t *tetris) tick() tea.Cmd {
	id := t.instance
	return tea.Tick(tetrisTickMS*time.Millisecond, func(time.Time) tea.Msg {
		return tetrisTickMsg{instance: id}
	})
}

func (t *tetris) reset() {
	pieces := t.pieces
	inst := t.instance
	*t = tetris{pieces: pieces, instance: inst}
	t.spawn()
}

func (t *tetris) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case tetrisTickMsg:
		if m.instance != t.instance {
			return nil
		}
		if !t.over && !t.paused {
			t.gravity()
		}
		return t.tick()
	case tea.KeyMsg:
		if t.over {
			if m.String() == "enter" || m.String() == " " {
				t.reset()
			}
			return nil
		}
		switch m.String() {
		case "left", "h", "a":
			t.tryMove(-1, 0)
		case "right", "l", "d":
			t.tryMove(1, 0)
		case "down", "j", "s":
			t.tryMove(0, 1)
		case "up", "k", "w":
			t.rotate()
		case " ":
			t.hardDrop()
		case "p":
			t.paused = !t.paused
		}
	}
	return nil
}

func (t *tetris) tryMove(dx, dy int) bool {
	p := t.cur
	p.pos.X += dx
	p.pos.Y += dy
	if t.collides(p) {
		return false
	}
	t.cur = p
	return true
}

// rotate spins the piece 90° clockwise. If the rotated shape collides,
// we try small horizontal nudges (a poor-man's wall kick) before giving
// up; this keeps the I-piece usable next to walls without implementing
// full SRS.
func (t *tetris) rotate() {
	p := t.cur
	p.shape = rotateCW(p.shape)
	for _, dx := range []int{0, -1, 1, -2, 2} {
		try := p
		try.pos.X += dx
		if !t.collides(try) {
			t.cur = try
			return
		}
	}
}

func (t *tetris) gravity() {
	if !t.tryMove(0, 1) {
		t.lock()
	}
}

func (t *tetris) hardDrop() {
	for t.tryMove(0, 1) {
	}
	t.lock()
}

func (t *tetris) lock() {
	for sy, row := range t.cur.shape {
		for sx, on := range row {
			if !on {
				continue
			}
			gx, gy := t.cur.pos.X+sx, t.cur.pos.Y+sy
			if gy >= 0 && gy < tetrisH && gx >= 0 && gx < tetrisW {
				t.grid[gy][gx] = t.cur.kind + 1
			}
		}
	}
	t.clearLines()
	t.spawn()
}

func (t *tetris) clearLines() {
	cleared := 0
	for y := tetrisH - 1; y >= 0; {
		full := true
		for x := 0; x < tetrisW; x++ {
			if t.grid[y][x] == 0 {
				full = false
				break
			}
		}
		if full {
			for yy := y; yy > 0; yy-- {
				t.grid[yy] = t.grid[yy-1]
			}
			t.grid[0] = [tetrisW]int{}
			cleared++
			// don't decrement y — recheck the row that just shifted down
		} else {
			y--
		}
	}
	t.lines += cleared
	switch cleared {
	case 1:
		t.score += 100
	case 2:
		t.score += 300
	case 3:
		t.score += 500
	case 4:
		t.score += 800
	}
}

func (t *tetris) spawn() {
	kind := rand.IntN(len(t.pieces))
	shape := t.pieces[kind].shape
	px := tetrisW/2 - len(shape)/2
	p := tetrominoPiece{kind: kind, shape: shape, pos: image.Pt(px, 0)}
	if t.collides(p) {
		t.over = true
		return
	}
	t.cur = p
}

func (t *tetris) collides(p tetrominoPiece) bool {
	for sy, row := range p.shape {
		for sx, on := range row {
			if !on {
				continue
			}
			gx, gy := p.pos.X+sx, p.pos.Y+sy
			if gx < 0 || gx >= tetrisW || gy >= tetrisH {
				return true
			}
			if gy < 0 {
				continue
			}
			if t.grid[gy][gx] != 0 {
				return true
			}
		}
	}
	return false
}

func (t *tetris) Render(styles Styles, innerW int) string {
	header := styles.HeaderTitle.Render(fmt.Sprintf("score %d  lines %d", t.score, t.lines))
	state := ""
	switch {
	case t.over:
		state = styles.ResultError.Render("game over · enter para reiniciar")
	case t.paused:
		state = styles.DialogWarning.Render("pausado")
	}
	if state != "" {
		header += "   " + state
	}

	// Compose a draw grid with the falling piece overlaid so render
	// doesn't have to special-case it twice.
	overlay := t.grid
	for sy, row := range t.cur.shape {
		for sx, on := range row {
			if !on {
				continue
			}
			gx, gy := t.cur.pos.X+sx, t.cur.pos.Y+sy
			if gx >= 0 && gx < tetrisW && gy >= 0 && gy < tetrisH {
				overlay[gy][gx] = t.cur.kind + 1
			}
		}
	}

	emptyStyle := lipgloss.NewStyle().Foreground(colBorder)
	var rows []string
	for y := 0; y < tetrisH; y++ {
		var line strings.Builder
		for x := 0; x < tetrisW; x++ {
			v := overlay[y][x]
			if v == 0 {
				line.WriteString(emptyStyle.Render("· "))
			} else {
				c := t.pieces[v-1].color
				line.WriteString(lipgloss.NewStyle().Foreground(c).Bold(true).Render("██"))
			}
		}
		rows = append(rows, line.String())
	}
	grid := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(colBorder).
		Render(strings.Join(rows, "\n"))
	grid = lipgloss.PlaceHorizontal(innerW, lipgloss.Center, grid)

	hint := styles.Hint.Render("← → mover · ↑ rotar · ↓ soft drop · space hard drop · p pausa")
	return strings.Join([]string{header, "", grid, "", hint}, "\n")
}

// buildTetrominoes returns the 7 standard pieces. Colors come from the
// active palette so each piece looks distinct against the chat-tinted
// dialog. Shapes are 0-rotation; rotateCW handles the rest.
func buildTetrominoes() []tetromino {
	return []tetromino{
		// I — long bar
		{shape: parseShape("....\n####\n....\n...."), color: colTertiary},
		// O — square (rotation no-op, but rotateCW still preserves it)
		{shape: parseShape("##\n##"), color: colWarning},
		// T
		{shape: parseShape(".#.\n###\n..."), color: colSecondary},
		// S
		{shape: parseShape(".##\n##.\n..."), color: colSuccess},
		// Z
		{shape: parseShape("##.\n.##\n..."), color: colDanger},
		// L
		{shape: parseShape("..#\n###\n..."), color: colAccent},
		// J
		{shape: parseShape("#..\n###\n..."), color: colPrimary},
	}
}

func parseShape(s string) [][]bool {
	rows := strings.Split(s, "\n")
	out := make([][]bool, len(rows))
	for i, r := range rows {
		row := make([]bool, len(r))
		for j, c := range r {
			row[j] = c == '#'
		}
		out[i] = row
	}
	return out
}

// rotateCW rotates a square boolean matrix 90° clockwise. Non-square
// inputs (the I-piece is 4x4 with padding, so it stays square) would be
// rotated as-if-square; we only ever pass squares.
func rotateCW(m [][]bool) [][]bool {
	n := len(m)
	out := make([][]bool, n)
	for i := range out {
		out[i] = make([]bool, n)
	}
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			out[x][n-1-y] = m[y][x]
		}
	}
	return out
}
