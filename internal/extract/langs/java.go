package langs

import (
	"strings"

	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/extract/generic"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

var javaBuiltinTypes = base.NewSet(
	"Object", "String", "CharSequence", "StringBuilder", "StringBuffer",
	"Number", "Byte", "Short", "Integer", "Long", "Float", "Double",
	"Boolean", "Character", "Void", "Class", "Enum", "Record", "Math",
	"System", "Thread", "Runnable", "Comparable", "Iterable", "Cloneable",
	"AutoCloseable", "Appendable", "Readable", "Process", "ProcessBuilder",
	"Runtime", "Package", "ThreadLocal", "InheritableThreadLocal",
	"Throwable", "Exception", "RuntimeException", "Error",
	"IllegalArgumentException", "IllegalStateException", "NullPointerException",
	"IndexOutOfBoundsException", "ArrayIndexOutOfBoundsException",
	"ClassCastException", "NumberFormatException", "ArithmeticException",
	"UnsupportedOperationException", "InterruptedException",
	"CloneNotSupportedException", "SecurityException", "StackOverflowError",
	"OutOfMemoryError", "AssertionError",
	"Collection", "List", "ArrayList", "LinkedList", "Vector", "Stack",
	"Set", "HashSet", "LinkedHashSet", "TreeSet", "SortedSet", "NavigableSet",
	"EnumSet", "Map", "HashMap", "LinkedHashMap", "TreeMap", "SortedMap",
	"NavigableMap", "Hashtable", "EnumMap", "Properties", "Queue", "Deque",
	"ArrayDeque", "PriorityQueue", "Iterator", "ListIterator", "Comparator",
	"Optional", "OptionalInt", "OptionalLong", "OptionalDouble", "Collections",
	"Arrays", "Objects", "Date", "Calendar", "Random", "UUID", "Scanner",
	"StringJoiner", "StringTokenizer", "BitSet", "Spliterator", "Locale",
	"NoSuchElementException", "ConcurrentModificationException",
	"Stream", "IntStream", "LongStream", "DoubleStream", "Collector",
	"Collectors",
	"Function", "BiFunction", "Consumer", "BiConsumer", "Supplier",
	"Predicate", "BiPredicate", "UnaryOperator", "BinaryOperator",
	"IntFunction", "ToIntFunction", "ToLongFunction", "ToDoubleFunction",
	"Callable", "Future", "CompletableFuture", "CompletionStage", "Executor",
	"ExecutorService", "Executors", "ScheduledExecutorService", "TimeUnit",
	"ConcurrentHashMap", "ConcurrentMap", "CopyOnWriteArrayList",
	"BlockingQueue", "CountDownLatch", "Semaphore", "CyclicBarrier",
	"AtomicInteger", "AtomicLong", "AtomicBoolean", "AtomicReference",
	"Instant", "Duration", "Period", "LocalDate", "LocalTime", "LocalDateTime",
	"ZonedDateTime", "OffsetDateTime", "ZoneId", "ZoneOffset", "DayOfWeek",
	"Month", "Year", "Clock", "DateTimeFormatter",
	"IOException", "UncheckedIOException", "FileNotFoundException", "File",
	"InputStream", "OutputStream", "Reader", "Writer", "BufferedReader",
	"BufferedWriter", "InputStreamReader", "OutputStreamWriter", "FileReader",
	"FileWriter", "PrintStream", "PrintWriter", "ByteArrayInputStream",
	"ByteArrayOutputStream", "Serializable", "Closeable", "Path", "Paths",
	"Files",
	"BigDecimal", "BigInteger",
)

var javaTypeParamScopes = base.NewSet("class_declaration", "interface_declaration", "record_declaration",
	"method_declaration", "constructor_declaration")

func javaTypeParams(n *tsx.Node) base.Set {
	out := base.Set{}

	for s := n; s != nil; s = s.Parent() {
		if !javaTypeParamScopes.Has(s.Type()) {
			continue
		}

		if tp := s.Field("type_parameters"); tp != nil {
			for _, p := range tp.Children() {
				if p.Type() != "type_parameter" {
					continue
				}

				if id := p.ChildOfType("type_identifier"); id != nil {
					out[id.Text()] = struct{}{}
				}
			}
		}
	}

	return out
}

func javaTypeRefs(n *tsx.Node, generic bool, out *[]typeRef, skip base.Set, qualified bool) {
	if n == nil {
		return
	}

	if skip == nil {
		skip = javaTypeParams(n)
	}

	switch n.Type() {
	case "integral_type", "floating_point_type", "boolean_type", "void_type":
		return
	case "type_identifier":
		if s := n.Text(); s != "" && !skip.Has(s) && !javaBuiltinTypes.Has(s) {
			*out = append(*out, typeRef{s, roleOf(generic)})
		}

		return
	case "scoped_type_identifier":
		raw := n.Text()
		simple := lastSeg(raw, ".")

		text := simple
		if qualified {
			text = raw
		}

		if text != "" && !javaBuiltinTypes.Has(simple) {
			*out = append(*out, typeRef{text, roleOf(generic)})
		}

		return
	case "generic_type":
		for _, c := range n.Children() {
			if c.Type() == "type_identifier" || c.Type() == "scoped_type_identifier" {
				raw := c.Text()
				simple := lastSeg(raw, ".")

				text := simple
				if qualified && c.Type() == "scoped_type_identifier" {
					text = raw
				}

				if text != "" && !javaBuiltinTypes.Has(simple) && (c.Type() == "scoped_type_identifier" || !skip.Has(simple)) {
					*out = append(*out, typeRef{text, roleOf(generic)})
				}

				break
			}
		}

		for _, c := range n.Children() {
			if c.Type() == "type_arguments" {
				for _, a := range c.NamedChildren() {
					javaTypeRefs(a, true, out, skip, qualified)
				}
			}
		}

		return
	}

	for _, c := range n.NamedChildren() {
		javaTypeRefs(c, generic, out, skip, qualified)
	}
}

func lastSeg(s, sep string) string {
	if i := strings.LastIndex(s, sep); i >= 0 {
		return s[i+len(sep):]
	}

	return s
}

func javaAnnotations(decl *tsx.Node) []*tsx.Node {
	mods := decl.ChildOfType("modifiers")
	if mods == nil {
		return nil
	}

	var out []*tsx.Node

	for _, c := range mods.Children() {
		if c.Type() == "marker_annotation" || c.Type() == "annotation" {
			out = append(out, c)
		}
	}

	return out
}

func javaAnnotationNames(decl *tsx.Node) [][2]string {
	var out [][2]string

	for _, a := range javaAnnotations(decl) {
		nn := a.Field("name")
		if nn == nil {
			nn = a.ChildOfType("identifier", "scoped_identifier", "type_identifier")
		}

		if nn != nil {
			raw := nn.Text()
			if s := lastSeg(raw, "."); s != "" {
				out = append(out, [2]string{s, raw})
			}
		}
	}

	return out
}

func javaAnnotationClassLiterals(decl *tsx.Node) []string {
	var out []string

	for _, a := range javaAnnotations(decl) {
		args := a.Field("arguments")
		if args == nil {
			continue
		}

		stack := []*tsx.Node{args}
		for len(stack) > 0 {
			cur := stack[len(stack)-1]
			stack = stack[:len(stack)-1]

			if cur.Type() == "class_literal" {
				var tn *tsx.Node
				if nc := cur.NamedChildren(); len(nc) > 0 {
					tn = nc[0]
				}

				var refs []typeRef
				javaTypeRefs(tn, false, &refs, nil, true)

				for _, r := range refs {
					out = append(out, r.name)
				}

				continue
			}

			stack = append(stack, cur.NamedChildren()...)
		}
	}

	return out
}

func javaImport(x *generic.Ctx, n *tsx.Node) [][2]string {
	var walkScoped func(c *tsx.Node) string
	walkScoped = func(c *tsx.Node) string {
		var parts []string

		for cur := c; cur != nil; {
			switch cur.Type() {
			case "scoped_identifier":
				if nn := cur.Field("name"); nn != nil {
					parts = append(parts, nn.Text())
				}

				cur = cur.Field("scope")
			case "identifier":
				parts = append(parts, cur.Text())
				cur = nil
			default:
				cur = nil
			}
		}

		for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
			parts[i], parts[j] = parts[j], parts[i]
		}

		return strings.Join(parts, ".")
	}

	for _, c := range n.Children() {
		if c.Type() != "scoped_identifier" && c.Type() != "identifier" {
			continue
		}

		p := walkScoped(c)
		segs := strings.Split(p, ".")

		mod := strings.Trim(strings.Trim(segs[len(segs)-1], "*"), ".")
		if mod == "" {
			if len(segs) > 1 {
				mod = segs[len(segs)-2]
			} else {
				mod = p
			}
		}

		if mod != "" {
			x.B.AddEdgeCtx(x.B.FileID, ids.MakeID(mod), "imports", n.Line(), "import")
		}

		break
	}

	return nil
}

func javaClassHook(x *generic.Ctx, n *tsx.Node, classID string, line int) {
	isJava := x.Cfg.Lang == "java"
	t := n.Type()

	emitParent := func(name, rel string) {
		if name != "" {
			x.B.AddEdge(classID, x.EnsureBase(name), rel, line)
		}
	}

	emitParentType := func(tn *tsx.Node, rel string) {
		var refs []typeRef
		javaTypeRefs(tn, false, &refs, nil, false)

		done := false
		for _, r := range refs {
			switch {
			case r.role == "type" && !done:
				emitParent(r.name, rel)
				done = true
			case r.role == "generic_arg":
				x.Ref(classID, x.EnsureNamed(r.name), line, "generic_arg")
			}
		}
	}

	if sup := n.Field("superclass"); sup != nil {
		if nc := sup.NamedChildren(); len(nc) > 0 {
			emitParentType(nc[0], "inherits")
		}
	}

	if ifs := n.Field("interfaces"); ifs != nil {
		for _, s := range ifs.Children() {
			if s.Type() == "type_list" {
				for _, tid := range s.NamedChildren() {
					emitParentType(tid, "implements")
				}
			}
		}
	}

	if t == "interface_declaration" {
		for _, c := range n.Children() {
			if c.Type() == "extends_interfaces" {
				for _, s := range c.Children() {
					if s.Type() == "type_list" {
						for _, tid := range s.NamedChildren() {
							emitParentType(tid, "inherits")
						}
					}
				}
			}
		}
	}

	seen := map[string]bool{}

	for _, a := range javaAnnotationNames(n) {
		name := a[0]
		if isJava && strings.Contains(a[1], ".") {
			name = a[1]
		}

		tgt := x.EnsureNamed(name)
		if tgt != classID && !seen[tgt] {
			x.B.AddEdgeCtx(classID, tgt, "references", line, "attribute")
			seen[tgt] = true
		}
	}

	for _, r := range javaAnnotationClassLiterals(n) {
		tgt := x.EnsureNamed(r)
		if tgt != classID && !seen[tgt] {
			x.B.AddEdgeCtx(classID, tgt, "references", line, "attribute")
			seen[tgt] = true
		}
	}

	if t == "record_declaration" {
		if comps := n.Field("parameters"); comps != nil {
			for _, c := range comps.Children() {
				var tn *tsx.Node

				switch c.Type() {
				case "formal_parameter":
					tn = c.Field("type")
				case "spread_parameter":
					for _, ch := range c.NamedChildren() {
						if ch.Type() != "modifiers" && ch.Type() != "variable_declarator" {
							tn = ch

							break
						}
					}
				default:
					continue
				}

				var refs []typeRef
				javaTypeRefs(tn, false, &refs, nil, false)

				for _, r := range refs {
					ctx := "field"
					if r.role == "generic_arg" {
						ctx = "generic_arg"
					}

					x.Ref(classID, x.EnsureNamed(r.name), c.Line(), ctx)
				}
			}
		}
	}
}

func javaFunctionHook(x *generic.Ctx, n *tsx.Node, funcID string, line int) {
	if x.Cfg.Lang != "java" {
		return
	}

	javaRecordMethodScope(x, n, funcID)

	if params := n.Field("parameters"); params != nil {
		for _, p := range params.Children() {
			if p.Type() != "formal_parameter" {
				continue
			}

			var refs []typeRef
			javaTypeRefs(p.Field("type"), false, &refs, nil, false)
			emitRefs(x, funcID, line, refs, "parameter_type")
		}
	}

	var refs []typeRef
	javaTypeRefs(n.Field("type"), false, &refs, nil, false)
	emitRefs(x, funcID, line, refs, "return_type")

	seen := map[string]bool{}

	for _, a := range javaAnnotationNames(n) {
		name := a[0]
		if strings.Contains(a[1], ".") {
			name = a[1]
		}

		tgt := x.EnsureNamed(name)
		if tgt != funcID && !seen[tgt] {
			x.B.AddEdgeCtx(funcID, tgt, "references", line, "attribute")
			seen[tgt] = true
		}
	}

	for _, r := range javaAnnotationClassLiterals(n) {
		tgt := x.EnsureNamed(r)
		if tgt != funcID && !seen[tgt] {
			x.B.AddEdgeCtx(funcID, tgt, "references", line, "attribute")
			seen[tgt] = true
		}
	}
}

func javaExtraWalk(x *generic.Ctx, n *tsx.Node, parentClass string) bool {
	if x.Cfg.Lang != "java" {
		return false
	}

	switch n.Type() {
	case "enum_constant":
		if parentClass == "" {
			return false
		}

		nn := n.Field("name")
		if nn == nil {
			return true
		}

		cid := ids.MakeID(parentClass, nn.Text())
		x.B.AddNode(cid, nn.Text(), n.Line())
		x.B.AddEdge(parentClass, cid, "case_of", n.Line())

		for _, c := range n.Children() {
			if c.Type() == "class_body" {
				for _, m := range c.Children() {
					x.Walk(m, cid)
				}
			}
		}

		return true
	case "field_declaration":
		if parentClass == "" {
			return false
		}

		javaRecordFields(x, parentClass, n)

		var refs []typeRef
		javaTypeRefs(n.Field("type"), false, &refs, nil, false)

		for _, r := range refs {
			ctx := "field"
			if r.role == "generic_arg" {
				ctx = "generic_arg"
			}

			x.Ref(parentClass, x.EnsureNamed(r.name), n.Line(), ctx)
		}

		return true
	case "annotation_type_element_declaration":
		if parentClass == "" {
			return false
		}

		var refs []typeRef
		javaTypeRefs(n.Field("type"), false, &refs, nil, true)

		for _, r := range refs {
			ctx := "return_type"
			if r.role == "generic_arg" {
				ctx = "generic_arg"
			}

			x.Ref(parentClass, x.EnsureNamed(r.name), n.Line(), ctx)
		}

		return true
	}

	return false
}

func javaCallName(_ *generic.Ctx, n *tsx.Node) (string, bool, string) {
	if n.Type() == "object_creation_expression" {
		if tn := n.Field("type"); tn != nil {
			raw := strings.TrimSpace(strings.SplitN(tn.Text(), "<", 2)[0])
			if raw != "" {
				return lastSeg(raw, "."), false, ""
			}
		}

		return "", false, ""
	}

	callee := ""
	if nn := n.Field("name"); nn != nil {
		callee = nn.Text()
	}

	recv := n.Field("object")
	if recv == nil {
		return callee, false, ""
	}

	switch recv.Type() {
	case "identifier":
		return callee, true, recv.Text()
	case "this", "super":
		return callee, true, recv.Type()
	case "field_access":
		o, f := recv.Field("object"), recv.Field("field")
		if o != nil && o.Type() == "this" && f != nil {
			return callee, true, "this." + f.Text()
		}
	}

	return callee, true, ""
}

var javaConfig = &generic.Config{
	Lang:    "java",
	Grammar: "java",
	ClassTypes: base.NewSet("class_declaration", "interface_declaration", "record_declaration",
		"enum_declaration", "annotation_type_declaration"),
	FunctionTypes:     base.NewSet("method_declaration", "constructor_declaration"),
	ImportTypes:       base.NewSet("import_declaration"),
	CallTypes:         base.NewSet("method_invocation", "object_creation_expression"),
	CallFunctionField: "name",
	FunctionBoundary:  base.NewSet("method_declaration", "constructor_declaration"),
	ImportHandler:     javaImport,
	ClassHook:         javaClassHook,
	FunctionHook:      javaFunctionHook,
	ExtraWalk:         javaExtraWalk,
	CallName:          javaCallName,
	DeferMember:       func(member bool, _ string) bool { return member },
	DecorateRawCall:   javaDecorateRawCall,
	PostProcess:       javaPostProcess,
}

// ExtractJava extracts a Java file.
func ExtractJava(path, root string, src []byte) *model.Extraction {
	return generic.Extract(javaConfig, path, root, src)
}
