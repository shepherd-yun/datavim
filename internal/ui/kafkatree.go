package ui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/kafka"
	"github.com/shepherd-yun/datavim/internal/session"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/tree"
)

// lagBatch is how many groups one GroupsLag call covers (design §4).
const lagBatch = 100

// The sections under a kafka connection.
const (
	secBrokers = "brokers"
	secTopics  = "topics"
	secGroups  = "groups"
	secSchemas = "schemas" // only with a schema_registry_url
)

var errKafkaUnsupported = errors.New("not supported for kafka")

// kafkaTree is what the tree of a kafka connection is built from, and the
// view settings that survive a refresh.
type kafkaTree struct {
	cluster              kafka.ClusterInfo
	topics               []kafka.Topic
	groups               []kafka.Group
	subjects             []kafka.Subject
	registry             bool              // the connection has a Schema Registry
	connect              []connectCluster  // its Kafka Connect clusters; nil until read
	connectOn            bool              // the connection has a connect_url
	errs                 map[string]string // section -> why it could not load
	lag                  map[string]int64
	showInternal, byName bool
	filter               map[string]string // section -> name filter
	gen                  int               // bumped by every load; late lag batches of an older one are dropped
	selectTopic          string            // a topic just created: selected once the tree is read again
}

// isKafka reports whether conn is a kafka connection.
func isKafka(b Backend, conn string) bool {
	cfg, ok := b.Connection(conn)
	return ok && cfg.Driver == "kafka"
}

// withKafka runs a read-only fn on conn's client; a lost connection is
// reset and fn retried once, as withConn does.
func withKafka(ctx context.Context, b Backend, conn string, fn func(kafka.Client) error) error {
	c, err := b.Kafka(ctx, conn)
	if err == nil {
		err = fn(c)
	}
	if !db.IsConnLost(err) {
		return err
	}
	slog.Info("kafka connection lost, reconnecting", "conn", conn, "err", err)
	b.Reset(conn)
	if c, err = b.Kafka(ctx, conn); err != nil {
		return err
	}
	return fn(c)
}

// kafkaTreeMsg is the load of a kafka connection's tree. err fails the
// whole load; errs are sections the user may not read; prompt is a secret
// the registry wants (the rest of the tree loads anyway).
type kafkaTreeMsg struct {
	node     *tree.Node
	cluster  kafka.ClusterInfo
	topics   []kafka.Topic
	groups   []kafka.Group
	subjects []kafka.Subject
	registry bool
	errs     map[string]string
	prompt   error
	err      error
}

func (m kafkaTreeMsg) failure() (string, error) { return m.node.Conn, m.err }

// sectionErr keeps a denied or unsupported section's error for its node;
// anything else fails the load.
func sectionErr(errs map[string]string, sec string, err error) error {
	if errors.Is(err, kafka.ErrDenied) || errors.Is(err, kafka.ErrUnsupported) {
		errs[sec] = err.Error()
		return nil
	}
	return err
}

// loadKafkaTree reads the brokers, topics and groups of n's connection.
func loadKafkaTree(b Backend, n *tree.Node) tea.Cmd {
	conn := n.Conn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msg := kafkaTreeMsg{node: n, errs: map[string]string{}}
		msg.err = withKafka(ctx, b, conn, func(c kafka.Client) error {
			var err error
			if msg.cluster, err = c.Cluster(ctx); sectionErr(msg.errs, secBrokers, err) != nil {
				return err
			}
			if msg.topics, err = c.Topics(ctx); sectionErr(msg.errs, secTopics, err) != nil {
				return err
			}
			if msg.groups, err = c.Groups(ctx); sectionErr(msg.errs, secGroups, err) != nil {
				return err
			}
			return nil
		})
		if msg.err == nil {
			msg.subjects, msg.registry, msg.prompt = loadSubjects(ctx, b, conn, msg.errs)
		}
		if msg.err != nil {
			slog.Warn("load kafka tree", "conn", conn, "err", msg.err)
		}
		return msg
	}
}

// loadSubjects reads the Schema Registry's subjects. A registry that
// fails is its section's error, the rest of the tree loads; one that wants
// a password is also returned, so it is asked for.
func loadSubjects(ctx context.Context, b Backend, conn string, errs map[string]string) ([]kafka.Subject, bool, error) {
	reg, err := b.SchemaRegistry(ctx, conn)
	if errors.Is(err, session.ErrNoRegistry) {
		return nil, false, nil
	}
	var subs []kafka.Subject
	if err == nil {
		subs, err = reg.Subjects(ctx)
	}
	var se *session.SecretRequiredError
	if errors.As(err, &se) {
		errs[secSchemas] = "the schema registry wants a password"
		return nil, true, err
	}
	if err != nil {
		errs[secSchemas] = err.Error()
	}
	return subs, true, nil
}

// kafkaTreeLoaded builds the sections and starts computing lag.
func (m *Model) kafkaTreeLoaded(msg kafkaTreeMsg) tea.Cmd {
	n := msg.node
	if errors.Is(msg.err, session.ErrPasswordRequired) {
		m.tree.CancelLoad(n)
		m.retryNode = n
		m.overlay = overlay.NewPassword(n.Conn)
		return nil
	}
	if m.userPrompt(msg.err) != nil {
		m.tree.CancelLoad(n)
		m.retryNode = n
		return nil
	}
	if msg.err != nil {
		m.tree.SetChildren(n, nil, msg.err)
		m.setStatus(msg.err.Error(), true)
		return nil
	}
	st := m.kafkaTrees[n.Conn]
	if st == nil {
		cfg, _ := m.backend.Connection(n.Conn)
		st = &kafkaTree{lag: map[string]int64{}, filter: map[string]string{}, showInternal: cfg.Params["show_internal"] == "true"}
		m.kafkaTrees[n.Conn] = st
	}
	st.gen++
	st.cluster, st.topics, st.groups, st.errs = msg.cluster, msg.topics, msg.groups, msg.errs
	st.subjects, st.registry = msg.subjects, msg.registry
	cfg, _ := m.backend.Connection(n.Conn)
	st.connectOn = cfg.Params["connect_url"] != ""
	m.buildKafkaTree(n.Conn)
	if o := m.userPrompt(msg.prompt); o != nil && m.overlay == nil {
		m.overlay = o
		m.retryNode = n // the secret reloads the tree
	}
	if st.selectTopic != "" {
		m.selectNewTopic(n.Conn, st.selectTopic)
		st.selectTopic = ""
	}
	return tea.Batch(m.lagCmd(n.Conn, st, 0), m.connectCmd(n.Conn, st))
}

// lagGroups are the groups whose lag can be computed, by name.
func (st *kafkaTree) lagGroups() []string {
	var out []string
	for _, g := range st.groups {
		if g.Type.HasLag() {
			out = append(out, g.Name)
		}
	}
	return out
}

// kafkaLagMsg is the lag of one batch of groups.
type kafkaLagMsg struct {
	conn string
	gen  int
	lag  map[string]int64
	next int // the index of the next batch in lagGroups
	err  error
}

func (m kafkaLagMsg) failure() (string, error) { return m.conn, m.err }

// lagCmd computes the lag of the batch of groups starting at from.
func (m *Model) lagCmd(conn string, st *kafkaTree, from int) tea.Cmd {
	names := st.lagGroups()
	if from >= len(names) {
		return nil
	}
	batch := names[from:min(from+lagBatch, len(names))]
	b, gen := m.backend, st.gen
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msg := kafkaLagMsg{conn: conn, gen: gen, next: from + len(batch)}
		msg.err = withKafka(ctx, b, conn, func(c kafka.Client) error {
			var err error
			msg.lag, err = c.GroupsLag(ctx, batch)
			return err
		})
		return msg
	}
}

func (m *Model) kafkaLagged(msg kafkaLagMsg) tea.Cmd {
	st := m.kafkaTrees[msg.conn]
	if st == nil || msg.gen != st.gen {
		return nil
	}
	if msg.err != nil {
		if !errors.Is(msg.err, session.ErrPasswordRequired) {
			m.setStatus("consumer group lag: "+msg.err.Error(), true)
		}
		return nil
	}
	for g, l := range msg.lag {
		st.lag[g] = l
	}
	m.buildKafkaTree(msg.conn)
	return m.lagCmd(msg.conn, st, msg.next)
}

// kafkaRoot is the root node of conn, or nil.
func (m *Model) kafkaRoot(conn string) *tree.Node {
	for _, r := range m.tree.Roots() {
		if r.Conn == conn {
			return r
		}
	}
	return nil
}

// nodeRef names a kafka node across rebuilds.
type nodeRef struct {
	kind         tree.Kind
	key, section string
}

func refOf(n *tree.Node) nodeRef { return nodeRef{kind: n.Kind, key: n.Key, section: n.Section} }

// buildKafkaTree (re)builds conn's sections from its kafkaTree, keeping
// what is open and the cursor.
func (m *Model) buildKafkaTree(conn string) {
	root, st := m.kafkaRoot(conn), m.kafkaTrees[conn]
	if root == nil || st == nil {
		return
	}
	open := map[nodeRef]bool{}
	var walk func([]*tree.Node)
	walk = func(nodes []*tree.Node) {
		for _, c := range nodes {
			if c.Expanded {
				open[refOf(c)] = true
			}
			walk(c.Children)
		}
	}
	walk(root.Children)
	var want *nodeRef
	if sel := m.tree.Selected(); sel != nil && sel.Conn == conn && sel != root {
		r := refOf(sel)
		want = &r
	}

	var brokers []*tree.Node
	for _, b := range st.cluster.Brokers {
		detail := []string{}
		if b.Rack != "" {
			detail = append(detail, "rack "+b.Rack)
		}
		if b.Controller {
			detail = append(detail, "★ controller")
		}
		brokers = append(brokers, &tree.Node{
			Kind: tree.KindBroker, Conn: conn, Section: secBrokers,
			Key: strconv.Itoa(int(b.ID)), Name: fmt.Sprintf("%d  %s:%d", b.ID, b.Host, b.Port), Detail: strings.Join(detail, " · "),
		})
	}
	topics := buildTopicNodes(conn, filterTopics(st.topics, st.filter[secTopics]), st.showInternal)
	groups := buildGroupNodes(conn, filterGroups(st.groups, st.filter[secGroups]), st.lag, st.byName)
	sections := []*tree.Node{
		m.kafkaSection(conn, secBrokers, "Brokers", len(brokers), brokers),
		m.kafkaSection(conn, secTopics, "Topics", countTopics(topics), topics),
		m.kafkaSection(conn, secGroups, "Consumer Groups", len(groups), groups),
	}
	if st.registry {
		subjects := buildSubjectNodes(conn, filterSubjects(st.subjects, st.filter[secSchemas]))
		sections = append(sections, m.kafkaSection(conn, secSchemas, "Schema Registry", len(subjects), subjects))
	}
	if st.connectOn {
		sections = append(sections, m.connectSection(conn, st))
	}
	sections = append(sections, &tree.Node{Kind: tree.KindKafkaACLs, Conn: conn, Section: secACLs, Key: secACLs, Name: "ACLs"})
	var reopen func([]*tree.Node)
	reopen = func(nodes []*tree.Node) {
		for _, c := range nodes {
			// a subject's versions load when it opens: rebuilt, it starts closed
			if open[refOf(c)] && c.Kind != tree.KindSchemaSubject {
				c.Expanded = true
			}
			reopen(c.Children)
		}
	}
	reopen(sections)
	m.tree.SetChildren(root, sections, nil)
	if want == nil {
		return
	}
	if !m.tree.SelectWhere(func(c *tree.Node) bool { return c.Conn == conn && refOf(c) == *want }) {
		// gone (filtered out, deleted): its section, else the connection
		if !m.tree.SelectWhere(func(c *tree.Node) bool {
			return c.Conn == conn && c.Kind == tree.KindKafkaSection && c.Section == want.section
		}) {
			m.tree.SelectWhere(func(c *tree.Node) bool { return c == root })
		}
	}
}

// kafkaSection is one section node with its children and error.
func (m *Model) kafkaSection(conn, sec, title string, count int, children []*tree.Node) *tree.Node {
	st := m.kafkaTrees[conn]
	n := &tree.Node{
		Kind: tree.KindKafkaSection, Conn: conn, Section: sec, Key: sec,
		Name: fmt.Sprintf("%s (%s)", title, groupDigits(int64(count))),
	}
	if f := st.filter[sec]; f != "" {
		n.Detail = "filter " + f
	}
	n.Err = st.errs[sec]
	tree.Adopt(n, children)
	return n
}

func countTopics(nodes []*tree.Node) int {
	n := 0
	for _, c := range nodes {
		if c.Kind == tree.KindTopicFolder {
			n += len(c.Children)
		} else {
			n++
		}
	}
	return n
}

func filterTopics(topics []kafka.Topic, f string) []kafka.Topic {
	if f == "" {
		return topics
	}
	f = strings.ToLower(f)
	return slices.DeleteFunc(slices.Clone(topics), func(t kafka.Topic) bool { return !strings.Contains(strings.ToLower(t.Name), f) })
}

func filterSubjects(subs []kafka.Subject, f string) []kafka.Subject {
	if f == "" {
		return subs
	}
	f = strings.ToLower(f)
	return slices.DeleteFunc(slices.Clone(subs), func(s kafka.Subject) bool { return !strings.Contains(strings.ToLower(s.Name), f) })
}

// buildSubjectNodes are the Schema Registry section's subjects.
func buildSubjectNodes(conn string, subs []kafka.Subject) []*tree.Node {
	out := make([]*tree.Node, len(subs))
	for i, s := range subs {
		out[i] = &tree.Node{
			Kind: tree.KindSchemaSubject, Conn: conn, Section: secSchemas, Key: s.Name, Name: s.Name,
			Detail: fmt.Sprintf("v%d %s", s.Latest, s.Type),
		}
	}
	return out
}

// versionKey is a version node's Key.
func versionKey(subject string, v int) string { return subject + "\x00" + strconv.Itoa(v) }

// subjectVersion splits a version node's Key.
func subjectVersion(n *tree.Node) (string, int) {
	subject, v, _ := strings.Cut(n.Key, "\x00")
	ver, _ := strconv.Atoi(v)
	return subject, ver
}

// loadSubjectVersions is the versions under a subject node.
func loadSubjectVersions(b Backend, n *tree.Node) tea.Cmd {
	conn, subject := n.Conn, n.Key
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msg := childrenLoadedMsg{node: n}
		reg, err := b.SchemaRegistry(ctx, conn)
		var vs []int
		if err == nil {
			vs, err = reg.Versions(ctx, subject)
		}
		for _, v := range vs {
			msg.children = append(msg.children, &tree.Node{
				Kind: tree.KindSchemaVersion, Conn: conn, Section: secSchemas, Key: versionKey(subject, v), Name: "v" + strconv.Itoa(v),
			})
		}
		msg.err = err
		return msg
	}
}

func filterGroups(groups []kafka.Group, f string) []kafka.Group {
	if f == "" {
		return groups
	}
	f = strings.ToLower(f)
	return slices.DeleteFunc(slices.Clone(groups), func(g kafka.Group) bool { return !strings.Contains(strings.ToLower(g.Name), f) })
}

// isInternalTopic reports whether a topic is Kafka's or a tool's own:
// marked internal, named __…, or _schemas.
func isInternalTopic(t kafka.Topic) bool {
	return t.Internal || strings.HasPrefix(t.Name, "__") || t.Name == "_schemas"
}

// topicPrefix is the part of name before its first . - or _, or "".
func topicPrefix(name string) string {
	if i := strings.IndexAny(name, ".-_"); i > 0 {
		return name[:i]
	}
	return ""
}

// buildTopicNodes lists topics by name, grouped one level deep into a
// folder per name prefix that at least two topics share. Internal topics
// are left out unless showInternal.
func buildTopicNodes(conn string, topics []kafka.Topic, showInternal bool) []*tree.Node {
	var shown []kafka.Topic
	for _, t := range topics {
		if showInternal || !isInternalTopic(t) {
			shown = append(shown, t)
		}
	}
	slices.SortFunc(shown, func(a, b kafka.Topic) int { return strings.Compare(a.Name, b.Name) })
	byPrefix := map[string]int{}
	for _, t := range shown {
		if p := topicPrefix(t.Name); p != "" {
			byPrefix[p]++
		}
	}
	folders := map[string]*tree.Node{}
	var out []*tree.Node
	for _, t := range shown {
		n := topicNode(conn, t)
		p := topicPrefix(t.Name)
		if byPrefix[p] < 2 {
			out = append(out, n)
			continue
		}
		f := folders[p]
		if f == nil {
			f = &tree.Node{Kind: tree.KindTopicFolder, Conn: conn, Section: secTopics, Key: p, Name: p}
			folders[p] = f
			out = append(out, f)
		}
		f.Children = append(f.Children, n)
	}
	for _, f := range folders {
		f.Detail = strconv.Itoa(len(f.Children))
		tree.Adopt(f, f.Children)
	}
	slices.SortStableFunc(out, func(a, b *tree.Node) int {
		return cmp.Compare(boolInt(a.Kind != tree.KindTopicFolder), boolInt(b.Kind != tree.KindTopicFolder))
	})
	return out
}

func topicNode(conn string, t kafka.Topic) *tree.Node {
	n := &tree.Node{
		Kind: tree.KindTopic, Conn: conn, Section: secTopics, Key: t.Name, Name: t.Name,
		Detail: strconv.Itoa(t.Partitions) + "p",
	}
	if t.UnderReplicated+t.Offline > 0 {
		n.Warn = true
		n.Detail += " · under-replicated"
	}
	return n
}

// buildGroupNodes lists groups by lag, largest first (groups without a
// known lag last, by name), or by name.
func buildGroupNodes(conn string, groups []kafka.Group, lag map[string]int64, byName bool) []*tree.Node {
	sorted := slices.Clone(groups)
	slices.SortFunc(sorted, func(a, b kafka.Group) int {
		if !byName {
			la, okA := lag[a.Name]
			lb, okB := lag[b.Name]
			if c := cmp.Or(cmp.Compare(boolInt(!okA), boolInt(!okB)), cmp.Compare(lb, la)); c != 0 {
				return c
			}
		}
		return strings.Compare(a.Name, b.Name)
	})
	out := make([]*tree.Node, len(sorted))
	for i, g := range sorted {
		var detail []string
		n := &tree.Node{Kind: tree.KindKafkaGroup, Conn: conn, Section: secGroups, Key: g.Name, Name: g.Name}
		if l, ok := lag[g.Name]; ok {
			detail = append(detail, "lag "+groupDigits(l))
			n.Warn = l > 0
		}
		if g.State != "" && g.State != "Stable" {
			detail = append(detail, g.State)
		}
		if g.Type != kafka.GroupClassic && g.Type != "" {
			detail = append(detail, string(g.Type))
		}
		n.Detail = strings.Join(detail, " · ")
		out[i] = n
	}
	return out
}

// kafkaTreeAction handles the tree keys that mean something else on a
// kafka connection; ok is false for the ones it leaves to the caller.
func (m *Model) kafkaTreeAction(a keymap.Action, n *tree.Node) (tea.Cmd, bool) {
	st := m.kafkaTrees[n.Conn]
	switch a {
	case keymap.ToggleInternal:
		if st != nil {
			st.showInternal = !st.showInternal
			m.buildKafkaTree(n.Conn)
		}
	case keymap.SortGroups:
		if st != nil {
			st.byName = !st.byName
			m.buildKafkaTree(n.Conn)
			if st.byName {
				m.setStatus("consumer groups by name", false)
			} else {
				m.setStatus("consumer groups by lag", false)
			}
		}
	case keymap.FilterPrompt:
		if st == nil || (n.Section != secTopics && n.Section != secGroups && n.Section != secSchemas) {
			m.setStatus("select Topics, Consumer Groups or Schema Registry to filter them", true)
			return nil, true
		}
		sec, title := n.Section, "Filter topics"
		switch sec {
		case secGroups:
			title = "Filter consumer groups"
		case secSchemas:
			title = "Filter subjects"
		}
		conn := n.Conn
		m.overlay = overlay.NewPrompt(title, "Name contains", st.filter[sec], func(text string) tea.Msg {
			return kafkaFilterMsg{conn: conn, section: sec, text: strings.TrimSpace(text)}
		})
	case keymap.FilterClear:
		if st != nil && st.filter[n.Section] != "" {
			delete(st.filter, n.Section)
			m.buildKafkaTree(n.Conn)
		}
	case keymap.Refresh:
		root := m.kafkaRoot(n.Conn)
		if root == nil || root.Loading {
			return nil, true
		}
		m.tree.StartLoad(root)
		return loadKafkaTree(m.backend, root), true
	case keymap.NewKey:
		return m.newTopicForm(n), true
	case keymap.DeleteNode:
		switch n.Kind {
		case tree.KindConn:
			return nil, false
		case tree.KindTopic:
			return m.askDeleteTopic(n), true
		case tree.KindKafkaGroup:
			return m.askDeleteGroup(n), true
		case tree.KindSchemaVersion:
			return m.askSoftDelete(n), true
		}
		m.setStatus("only topics, groups, schema versions (and the connection) can be deleted here", true)
	case keymap.Open:
		switch n.Kind {
		case tree.KindKafkaACLs:
			return m.openACLs(n.Conn), true
		case tree.KindConnector:
			cluster, name := connectorOf(n)
			return m.openConnector(n.Conn, cluster, name), true
		case tree.KindTopic:
			return m.openTopic(n.Conn, n.Key), true
		case tree.KindKafkaGroup:
			return m.openGroup(n.Conn, n.Key), true
		case tree.KindSchemaSubject:
			return m.openSchema(n.Conn, n.Key, -1), true
		case tree.KindSchemaVersion:
			subject, v := subjectVersion(n)
			return m.openSchema(n.Conn, subject, v), true
		}
		return nil, false
	default:
		return nil, false
	}
	return nil, true
}

// kafkaFilterMsg sets the name filter of a section.
type kafkaFilterMsg struct{ conn, section, text string }

func (m *Model) kafkaFiltered(msg kafkaFilterMsg) {
	st := m.kafkaTrees[msg.conn]
	if st == nil {
		return
	}
	if msg.text == "" {
		delete(st.filter, msg.section)
	} else {
		st.filter[msg.section] = msg.text
	}
	m.buildKafkaTree(msg.conn)
}

// kafkaHit is a topic or group the global search found.
type kafkaHit struct {
	conn, name string
	group      bool
	prefix     bool // the name starts with the query: listed before the catalog hits
}

func (h kafkaHit) item() overlay.SearchItem {
	kind := "topic"
	if h.group {
		kind = "group"
	}
	return overlay.SearchItem{Kind: kind, Label: h.name, Where: h.conn}
}

// searchKafka finds the topics and groups (of conn, unless empty) whose
// name contains text, ignoring case: only in the trees already loaded, so
// searching reads nothing.
func (m *Model) searchKafka(conn, text string) []kafkaHit {
	q := strings.ToLower(strings.TrimSpace(text))
	if q == "" {
		return nil
	}
	var out []kafkaHit
	for _, c := range m.backend.Connections() {
		st := m.kafkaTrees[c.Name]
		if st == nil || conn != "" && c.Name != conn || c.Driver != "kafka" {
			continue
		}
		add := func(name string, group bool) {
			lower := strings.ToLower(name)
			if i := strings.Index(lower, q); i >= 0 {
				out = append(out, kafkaHit{conn: c.Name, name: name, group: group, prefix: i == 0})
			}
		}
		for _, t := range st.topics {
			if st.showInternal || !isInternalTopic(t) {
				add(t.Name, false)
			}
		}
		for _, g := range st.groups {
			add(g.Name, true)
		}
	}
	slices.SortStableFunc(out, func(a, b kafkaHit) int {
		return cmp.Or(cmp.Compare(boolInt(!a.prefix), boolInt(!b.prefix)), cmp.Compare(len(a.name), len(b.name)), strings.Compare(a.name, b.name))
	})
	return out[:min(len(out), searchLimit)]
}

// openKafkaHit opens a found topic or group.
func (m *Model) openKafkaHit(h kafkaHit) tea.Cmd {
	if h.group {
		return m.openGroup(h.conn, h.name)
	}
	return m.openTopic(h.conn, h.name)
}
