package nodeprobe

import (
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
	"github.com/expr-lang/expr/vm"
)

const (
	defaultMeasureTimeout = 5 * time.Second
	defaultSamples        = 1
	defaultConcurrency    = 4
	defaultWindow         = 6
	defaultTrafficWindow  = 10 * time.Minute
	defaultScreenInterval = time.Hour
	defaultCheckInterval  = 5 * time.Minute
	defaultCooldown       = 15 * time.Second
	defaultDomesticTime   = 5 * time.Second

	defaultEligible = "true"
	defaultQualify  = "true"
	defaultScore    = "node.index"
	defaultSwitch   = "!current.qualified || best.score < current.score"
	defaultPenalty  = "0"
)

var measureName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

type measure struct {
	name        string
	kind        string
	targets     []string
	rotate      bool
	bytes       uint32
	timeout     time.Duration
	samples     int
	concurrency int
	window      int
}

const (
	kindDownload = "download"
	kindRequest  = "request"
)

func newMeasure(name string, options option.NodeProbeMeasureOptions) (*measure, error) {
	if !measureName.MatchString(name) {
		return nil, E.New("invalid measure name: ", name)
	}
	m := &measure{
		name:        name,
		kind:        options.Kind,
		targets:     options.Targets,
		bytes:       options.Bytes,
		timeout:     time.Duration(options.Timeout),
		samples:     options.Samples,
		concurrency: options.Concurrency,
		window:      options.Window,
	}
	switch m.kind {
	case kindDownload:
		if m.bytes == 0 {
			return nil, E.New("measure ", name, ": missing bytes")
		}
	case kindRequest:
		if m.bytes != 0 {
			return nil, E.New("measure ", name, ": bytes is only for download")
		}
	case "":
		return nil, E.New("measure ", name, ": missing kind")
	default:
		return nil, E.New("measure ", name, ": unknown kind: ", m.kind)
	}
	if len(m.targets) == 0 {
		return nil, E.New("measure ", name, ": missing targets")
	}
	switch options.TargetMode {
	case "", "all":
	case "rotate":
		m.rotate = true
	default:
		return nil, E.New("measure ", name, ": unknown target_mode: ", options.TargetMode)
	}
	if m.timeout <= 0 {
		m.timeout = defaultMeasureTimeout
	}
	if m.samples <= 0 {
		m.samples = defaultSamples
	}
	if m.concurrency <= 0 {
		m.concurrency = defaultConcurrency
	}
	if m.window <= 0 {
		m.window = defaultWindow
	}
	return m, nil
}

// roundTargets returns the targets one round measures.
func (m *measure) roundTargets(round int) []string {
	if m.rotate {
		return []string{m.targets[round%len(m.targets)]}
	}
	return m.targets
}

type clause struct {
	source  string
	program *vm.Program
}

// Expression is the effective source of one policy expression.
type Expression struct {
	Name   string
	Source string
}

type policy struct {
	name             string
	screenMeasures   []string
	screenInterval   time.Duration
	onNetworkChange  bool
	checkMeasures    []string
	checkInterval    time.Duration
	onTrafficFailure bool
	cooldown         time.Duration

	eligible *vm.Program
	qualify  []clause
	score    *vm.Program
	switchTo *vm.Program
	penalty  *vm.Program

	expressions []Expression
	measures    []string // screen and check measures, deduplicated
}

func newPolicy(name string, options option.NodeProbePolicyOptions, measures map[string]*measure) (*policy, error) {
	if options.Screen == nil {
		return nil, E.New("policy ", name, ": missing screen")
	}
	p := &policy{
		name:            name,
		screenMeasures:  options.Screen.Measures,
		screenInterval:  time.Duration(options.Screen.Interval),
		onNetworkChange: options.Screen.OnNetworkChange,
	}
	if len(p.screenMeasures) == 0 {
		return nil, E.New("policy ", name, ": missing screen measures")
	}
	if p.screenInterval <= 0 {
		p.screenInterval = defaultScreenInterval
	}
	if options.Check != nil {
		p.checkMeasures = options.Check.Measures
		p.checkInterval = time.Duration(options.Check.Interval)
		p.onTrafficFailure = options.Check.OnTrafficFailure
		p.cooldown = time.Duration(options.Check.Cooldown)
		if len(p.checkMeasures) == 0 {
			return nil, E.New("policy ", name, ": missing check measures")
		}
		if p.checkInterval <= 0 {
			p.checkInterval = defaultCheckInterval
		}
		if p.cooldown <= 0 {
			p.cooldown = defaultCooldown
		}
	}
	for _, measureName := range append(slices.Clone(p.screenMeasures), p.checkMeasures...) {
		if measures[measureName] == nil {
			return nil, E.New("policy ", name, ": unknown measure: ", measureName)
		}
		if !slices.Contains(p.measures, measureName) {
			p.measures = append(p.measures, measureName)
		}
	}
	source := func(value, fallback string) string {
		if strings.TrimSpace(value) == "" {
			return fallback
		}
		return value
	}
	eligible := source(options.Eligible, defaultEligible)
	qualify := source(options.Qualify, defaultQualify)
	score := source(options.Score, defaultScore)
	switchTo := source(options.Switch, defaultSwitch)
	penalty := source(options.Penalty, defaultPenalty)
	p.expressions = []Expression{
		{"eligible", eligible}, {"qualify", qualify}, {"score", score}, {"switch", switchTo}, {"penalty", penalty},
	}
	var err error
	if p.eligible, err = compile(name, "eligible", eligible, NodeEnv{}, expr.AsBool(), measures); err != nil {
		return nil, err
	}
	if p.qualify, err = compileClauses(name, qualify, measures); err != nil {
		return nil, err
	}
	if p.score, err = compile(name, "score", score, NodeEnv{}, expr.AsFloat64(), measures); err != nil {
		return nil, err
	}
	if p.switchTo, err = compile(name, "switch", switchTo, SwitchEnv{}, expr.AsBool(), measures); err != nil {
		return nil, err
	}
	if p.penalty, err = compile(name, "penalty", penalty, PenaltyEnv{}, expr.AsFloat64(), measures); err != nil {
		return nil, err
	}
	return p, nil
}

// compileClauses splits qualify at top-level conjunctions, so a failed verdict
// can name the clause that failed.
func compileClauses(policyName, source string, measures map[string]*measure) ([]clause, error) {
	_, err := compile(policyName, "qualify", source, NodeEnv{}, expr.AsBool(), measures)
	if err != nil {
		return nil, err
	}
	tree, err := parser.Parse(source)
	if err != nil {
		return nil, E.Cause(err, "policy ", policyName, ": qualify")
	}
	var nodes []ast.Node
	var split func(node ast.Node)
	split = func(node ast.Node) {
		if binary, isBinary := node.(*ast.BinaryNode); isBinary && (binary.Operator == "&&" || binary.Operator == "and") {
			split(binary.Left)
			split(binary.Right)
			return
		}
		nodes = append(nodes, node)
	}
	split(tree.Node)
	clauses := make([]clause, 0, len(nodes))
	for _, node := range nodes {
		clauseSource := node.String()
		program, err := compile(policyName, "qualify", clauseSource, NodeEnv{}, expr.AsBool(), measures)
		if err != nil {
			return nil, err
		}
		clauses = append(clauses, clause{source: clauseSource, program: program})
	}
	return clauses, nil
}

func compile(policyName, name, source string, env any, expect expr.Option, measures map[string]*measure) (*vm.Program, error) {
	checker := &measureChecker{measures: measures}
	program, err := expr.Compile(source, expr.Env(env), expect, expr.Patch(checker))
	if err != nil {
		return nil, E.Cause(err, "policy ", policyName, ": ", name)
	}
	if checker.unknown != "" {
		return nil, E.New("policy ", policyName, ": ", name, ": unknown measure: ", checker.unknown)
	}
	return program, nil
}

// measureChecker rejects m.<name> for undefined measures, which would
// otherwise read as an empty window at run time.
type measureChecker struct {
	measures map[string]*measure
	unknown  string
}

func (c *measureChecker) Visit(node *ast.Node) {
	member, isMember := (*node).(*ast.MemberNode)
	if !isMember || c.unknown != "" {
		return
	}
	property, isString := member.Property.(*ast.StringNode)
	if !isString || !isMeasureMap(member.Node) {
		return
	}
	if c.measures[property.Value] == nil {
		c.unknown = property.Value
	}
}

func isMeasureMap(node ast.Node) bool {
	switch typed := node.(type) {
	case *ast.IdentifierNode:
		return typed.Value == "m"
	case *ast.MemberNode:
		property, isString := typed.Property.(*ast.StringNode)
		return isString && property.Value == "m"
	}
	return false
}
