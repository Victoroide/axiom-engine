# Curriculum Coverage Report: AP Computer Science A (ap-csa-fall-2025)

> **Provisional Status:** Subject-specialist verification is pending. Mapping of the embedded seed graph against AP Computer Science A (Fall 2025 CED and January 2026 Clarifications).

## Summary

| Field | Value |
|---|---|
| Framework | AP Computer Science A |
| Edition | `ap-csa-fall-2025` |
| Effective Term | Fall 2025 |
| Clarifications Revision | 2026-01-29 |
| Course Language | en (Java) |
| Graph Path | `internal/graph/seed.json` |
| Graph SHA-256 | `7a9f78f6a80eeb7bc6e21afbf71b735621604615f0faf9c881d600ab5a7d84aa` |
| Graph Size | 23 concepts, 20 tuples (17 `depends_on`, 3 `is_a`), 4 connected component(s) |
| Total Denominator | 73 distinct learning objectives |
| Status Counts | 0 mapped, 24 partial, 49 missing |
| Evidence Relations | 6 `depends_on`, 0 `is_a` |
| Unresolved Disputes | 13 |
| Outside-Course Concepts | 15 |
| Blank Dispute Form | `evaluation/curriculum/dispute-form.md` |

## Authoritative Sources

| Source | Publisher | Revision | SHA-256 | Link |
|---|---|---|---|---|
| AP Computer Science A Course and Exam Description (Effective Fall 2025) (`ced`) | College Board | 2025 edition (PDF revised 2025-06-17) | `c81501ad5a022c83f1ca51119cccd1907b1394e1416d8c2a01bd43a38ab16486` | [PDF](https://apcentral.collegeboard.org/media/pdf/ap-computer-science-a-course-and-exam-description.pdf) |
| AP Computer Science A Clarifications and Corrections to the Course and Exam Description (`clarifications`) | College Board | 2026-01-29 PDF revision (career-preparation wording implemented in August 2025; resource links and Progress Check edits announced for summer 2026; all 73 learning objectives unchanged) | `d39626653656bf0baa0127dc8c3fa50c5e7a41bc7b3747e43d997f69a2d18e56` | [PDF](https://apcentral.collegeboard.org/media/pdf/ap-computer-science-a-course-and-exam-description-clarification.pdf) |

## Coverage by Unit

| Unit | Title | Objectives | Mapped | Partial | Missing | Unresolved Disputes |
|---|---|---:|---:|---:|---:|---:|
| Unit 1 | Using Objects and Methods | 28 | 0 | 10 | 18 | 6 |
| Unit 2 | Selection and Iteration | 14 | 0 | 1 | 13 | 0 |
| Unit 3 | Class Creation | 10 | 0 | 6 | 4 | 6 |
| Unit 4 | Data Collections | 21 | 0 | 7 | 14 | 1 |

## Objective Crosswalk

| Objective | Unit | Page(s) | Original Summary | Status | Graph Concepts | Directed Relations | Rationale | Disputes |
|---|---:|---|---|---|---|---|---|---|
| `1.1.A` | 1 | 30 | Design and express sequential everyday algorithms in plain prose or diagrams. | `missing (isolated)` | - | - | The seed has no topic for planning an everyday sequence in prose or a diagram. A standalone planning topic has no evidenced tuple. | - |
| `1.1.B` | 1 | 30 | Describe how source code is authored, checked by a compiler, and executed, including IDE support. | `missing (isolated)` | - | - | The seed has no compiler or IDE topic to explain the path from source code to execution. A standalone compilation topic has no evidenced tuple. | - |
| `1.1.C` | 1 | 30-31 | Distinguish compile-time syntax errors, logic errors found by testing, run-time failures, and exceptions. | `missing` | - | - | The seed has no compilation, testing, or exception topics for distinguishing syntax errors, logic errors, and run-time failures. | - |
| `1.2.A` | 1 | 32 | Choose between primitive and reference data-type categories for a stated problem requirement. | `missing` | - | - | Variables identifies named storage, but the seed has no data-type concepts for choosing primitive versus reference types. | - |
| `1.2.B` | 1 | 32 | Declare typed primitive variables (int, double, boolean) to store numeric and truth values. | `partial` | Variables | - | Seed includes Variables, but omits Data Types, primitive categories, and Java int/double/boolean constraints. | - |
| `1.3.A` | 1 | 33 | Write and trace console output statements using System.out.print and System.out.println. | `missing` | - | - | The seed has no output topic for writing or tracing System.out.print and System.out.println calls. | - |
| `1.3.B` | 1 | 33 | Construct and evaluate string literals including escape sequences for quotes, backslashes, and newlines. | `missing` | - | - | The seed has no Strings topic to cover literal syntax or escapes for quotes, backslashes, and newlines. | - |
| `1.3.C` | 1 | 33-34 | Write and evaluate int and double arithmetic expressions using +, -, *, /, %, precedence rules, and integer division by zero. | `missing` | - | - | The seed has no arithmetic expressions or operator topics for precedence, numeric operations, and integer division. | - |
| `1.4.A` | 1 | 35 | Initialize and update variables with = expressions, including null references, and trace stored values. | `partial` | Variables | - | Seed includes Variables, but omits assignment operators, Expressions, and null references. | - |
| `1.4.B` | 1 | 35 | Write Java code that reads input supplied to a program. | `missing` | - | - | The seed has no input or I/O topic to support writing Java code that reads data. | - |
| `1.5.A` | 1 | 36 | Apply explicit (int) and (double) casts and implicit widening in arithmetic, including truncation and rounding patterns. | `missing` | - | - | The seed has no numeric-conversion topic to distinguish explicit casts, widening, and truncation. | - |
| `1.5.B` | 1 | 36-37 | Identify integer overflow when 32-bit int calculations exceed Integer.MIN_VALUE through Integer.MAX_VALUE. | `missing` | - | - | The seed has no model of fixed-width integers or the bounds that determine when int arithmetic overflows. | - |
| `1.5.C` | 1 | 37 | Explain floating-point round-off errors caused by finite double representation precision. | `missing` | - | - | The seed has no topic for how finite double precision causes rounding errors. | - |
| `1.6.A` | 1 | 38 | Write and trace compound assignment (+=, -=, *=, /=, %=) and postfix ++ and -- variable updates. | `missing` | - | - | Variables does not document arithmetic assignment, postfix increments or decrements, or the effect of tracing those updates. | - |
| `1.7.A` | 1 | 39 | Read library and API documentation to determine a class's data attributes, methods, and package grouping. | `partial` | Classes, Objects | - | Seed includes Classes and Objects, but omits API specifications, libraries, and Encapsulation. | DSP-SEED-1.7.A-cls-obj (unresolved: disputed_prerequisite) |
| `1.8.A` | 1 | 40 | Document code behavior, preconditions, and postconditions with line, block, and Javadoc comments ignored by the compiler. | `missing (isolated)` | - | - | The seed has no comments or Javadoc topic for documenting behavior and method contracts. A standalone documentation topic has no evidenced tuple. | - |
| `1.9.A` | 1 | 41 | Select the matching method to invoke from documentation and method parameter signatures. | `partial` | Functions | - | Seed includes Functions, but omits method signatures, parameter types, and procedural abstraction documentation. | - |
| `1.9.B` | 1 | 41-42 | Explain method invocation rules for void and non-void methods, call-by-value arguments, overloading, and control flow return. | `partial` | Functions | - | Seed includes Functions, but omits void/non-void returns, call-by-value arguments, and overloading. | - |
| `1.10.A` | 1 | 43 | Invoke static class methods with the class name and dot operator and trace their return values. | `missing` | - | - | The seed has Classes and Functions, but no static-method topic for calls made through a class name. | - |
| `1.11.A` | 1 | 44 | Write and evaluate expressions calling Math.abs, Math.pow, Math.sqrt, and scaled Math.random ranges. | `missing` | - | - | The seed has no numerical library topic for Math functions or transforming the range of Math.random results. | - |
| `1.12.A` | 1 | 45 | Explain classes as object blueprints, Object as Java's universal superclass, and conceptual superclass/subclass relationships without designing hierarchies. | `partial` | Classes, Objects | - | Seed includes Classes and Objects, but omits Object root inheritance while linking out-of-scope Inheritance and taxonomic Abstract Classes is_a Classes. | DSP-SEED-1.12.A-abs-isa (unresolved: taxonomic_not_prerequisite); DSP-SEED-1.12.A-cls-obj (unresolved: disputed_prerequisite); DSP-SEED-1.12.A-inh (unresolved: out_of_scope_dependency) |
| `1.12.B` | 1 | 45 | Declare variables of reference types that hold references to objects. | `partial` | Objects, Variables | Objects -[depends_on]-&gt; Variables | Seed includes Objects and Variables, but omits Java reference-type declarations. | - |
| `1.13.A` | 1 | 46 | Match a constructor call to its signature among potentially overloaded constructors. | `missing` | - | - | The Classes and Objects tuple leaves constructor signatures and overload selection unspecified. | - |
| `1.13.B` | 1 | 46 | Declare reference variables typed for objects or initialized to null. | `partial` | Objects, Variables | Objects -[depends_on]-&gt; Variables | Seed includes Objects and Variables, but omits reference variable declarations and null. | - |
| `1.13.C` | 1 | 46-47 | Instantiate objects with new, passing constructor arguments by value and tracing constructor execution. | `partial` | Classes, Objects | - | Seed includes Classes and Objects, but omits constructors, new instantiation, and call-by-value initialization. | DSP-SEED-1.13.C-cls-obj (unresolved: disputed_prerequisite) |
| `1.14.A` | 1 | 48 | Invoke instance methods on object references with the dot operator, trace outcomes, and recognize NullPointerException on null targets. | `partial` | Classes, Functions, Objects | - | Seed includes Classes, Functions, and Objects without instance-method dispatch or NullPointerException. | DSP-SEED-1.14.A-cls-obj (unresolved: disputed_prerequisite) |
| `1.15.A` | 1 | 49-50 | Create immutable String objects, concatenate strings with primitive or toString values, and trace results. | `missing` | - | - | The seed has no Strings topic for immutability, concatenation, or conversion to text. | - |
| `1.15.B` | 1 | 50-51 | Invoke String methods (length, substring, indexOf, equals, compareTo) and recognize out-of-bounds index exceptions. | `missing` | - | - | The seed has no string operations to trace substring extraction, search, comparison, or invalid indexes. | - |
| `2.1.A` | 2 | 61 | Express everyday algorithms combining sequence, conditional choice, and repetition in prose or diagrams. | `missing` | - | - | The seed has no control-flow topic linking sequence, decisions, and repetition for everyday algorithms. | - |
| `2.2.A` | 2 | 62 | Write and evaluate Boolean expressions that compare primitive values or reference identity with == and !=, and test numeric ordering with &lt;, &gt;, &lt;=, and &gt;=. | `missing` | - | - | The seed has no relational operators or Boolean expressions for comparing numeric values or checking object identity. | - |
| `2.3.A` | 2 | 63 | Implement and trace one-way if and two-way if-else branching statements. | `missing` | - | - | The seed has no selection topic that shows a Boolean guard, an if branch, or an else branch. | - |
| `2.4.A` | 2 | 64 | Implement and trace nested if and multiway if-else-if selection chains. | `missing` | - | - | The seed has no conditional logic that could support nested choices or else-if chains. | - |
| `2.5.A` | 2 | 65 | Construct and trace compound Boolean logic with !, &amp;&amp;, and \|\|, operator precedence, and short-circuit evaluation. | `missing` | - | - | The seed has no Boolean operator or evaluation topic for compound conditions and short-circuiting. | - |
| `2.6.A` | 2 | 66 | Prove Boolean equivalence using truth tables and De Morgan's laws. | `missing` | - | - | The seed has no Boolean equivalence, truth-table, or De Morgan's law topic. | - |
| `2.6.B` | 2 | 66 | Compare object references for alias identity (==, !=, null) versus attribute equality (equals). | `partial` | Objects | - | Seed includes Objects, but omits Boolean comparisons, == alias checks, and equals. | - |
| `2.7.A` | 2 | 67 | Recognize problems needing iteration and diagnose zero-iteration conditions, infinite loops, and off-by-one bugs. | `missing` | - | - | The seed has no iteration topic for deciding when to loop or identifying zero iterations and termination errors. | - |
| `2.7.B` | 2 | 67 | Write and trace while loop repetition governed by a pre-checked Boolean condition. | `missing` | - | - | The seed has no while-loop topic for tracing a pre-checked condition and its repeated body. | - |
| `2.8.A` | 2 | 68 | Write and trace for loops with header initialization, guard, and update, and convert between for and while. | `missing` | - | - | The seed has no for-loop topic for tracing initialization, condition, and update or comparing the equivalent while loop. | - |
| `2.9.A` | 2 | 69 | Implement and trace standard non-collection algorithms for divisibility, integer digits, counts, min/max, and sums or averages. | `missing` | - | - | The seed has no numeric iteration topic for algorithms on digits, divisibility, counts, extrema, or sums. | - |
| `2.10.A` | 2 | 70 | Implement and trace iterative algorithms that search, count substrings, and reverse strings. | `missing` | - | - | The seed has no Strings or iteration topic for searching substrings, counting matches, or reversing text. | - |
| `2.11.A` | 2 | 71 | Implement and trace nested loops where the inner loop finishes completely on each outer iteration. | `missing` | - | - | The seed has no nested-loop topic that shows how the inner loop runs during each outer step. | - |
| `2.12.A` | 2 | 72 | Trace loops to count statement executions and compare iterative run-time efficiency informally. | `missing` | - | - | The seed names Binary Search and Merge Sort, but has no statement-count evidence for comparing the efficiency of iterative code. | - |
| `3.1.A` | 3 | 80-81 | Plan class-based programs in prose or diagrams specifying data attributes, procedural decomposition, and method behaviors. | `partial` | Classes, Functions, Objects | - | Seed includes Classes, Functions, and Objects across disconnected components, but omits Encapsulation, design diagrams, and decomposition. | DSP-SEED-3.1.A-abs-isa (unresolved: taxonomic_not_prerequisite); DSP-SEED-3.1.A-cls-obj (unresolved: disputed_prerequisite) |
| `3.2.A` | 3 | 82 | Analyze software reliability, beneficial and harmful societal, economic, and cultural impacts, and open-source versus proprietary licensing. | `missing (isolated)` | - | - | The seed has no reliability, software testing, social impact, or licensing topic. The standalone ethics requirements have no evidenced tuple. | - |
| `3.3.A` | 3 | 83 | Apply public and private access modifiers to classes, instance fields, constructors, and methods to enforce encapsulation. | `partial` | Classes | - | Seed includes Classes without Encapsulation or public/private access control. | DSP-SEED-3.3.A-cls-obj (unresolved: disputed_prerequisite) |
| `3.4.A` | 3 | 84 | Declare private instance fields, initialize object state in constructors (copying mutable arguments), and apply default constructor rules. | `partial` | Classes, Objects, Variables | Objects -[depends_on]-&gt; Variables | Seed includes Classes, Objects, and Variables without constructor initialization or default field values. | DSP-SEED-3.4.A-cls-obj (unresolved: disputed_prerequisite) |
| `3.5.A` | 3 | 85-86 | Author void mutators and typed non-void accessor or computation methods using primitive parameters passed by value and return flow. | `partial` | Classes, Functions, Variables | - | Seed includes Classes, Functions, and Variables without accessor/mutator definitions or return rules. | DSP-SEED-3.5.A-cls-obj (unresolved: disputed_prerequisite) |
| `3.6.A` | 3 | 87 | Write and trace methods taking and returning object references, observing aliasing, mutable state effects, and same-class private access. | `partial` | Classes, Functions, Objects | - | Seed includes Classes, Functions, and Objects without reference parameter/return aliasing. | DSP-SEED-3.6.A-cls-obj (unresolved: disputed_prerequisite) |
| `3.7.A` | 3 | 88 | Write static class methods that operate on static state and class helpers without direct instance state access. | `missing` | - | - | Classes and Functions supply no evidence for writing static helpers or using class state independently of an instance. | - |
| `3.7.B` | 3 | 88 | Declare shared static class variables and final constants with appropriate visibility. | `missing` | - | - | Classes and Variables supply no evidence about shared static fields, visibility, or final constants. | - |
| `3.8.A` | 3 | 89 | Determine variable visibility across blocks, constructors, methods, and parameters, including local shadowing of instance fields. | `partial` | Functions, Variables | - | Seed includes Functions and Variables without Scope or shadowing rules. | - |
| `3.9.A` | 3 | 90 | Use this within instance methods and constructors to refer to or pass the active receiver object (unavailable in static methods). | `missing` | - | - | Objects does not specify the Java this expression or how methods refer to and pass their receiver. | - |
| `4.1.A` | 4 | 100 | Explain personal privacy hazards from collecting and retaining user data and a developer's responsibility to safeguard privacy. | `missing (isolated)` | - | - | The seed has no topic on collecting or retaining personal data or the responsibility to protect it. A standalone privacy topic has no evidenced tuple. | - |
| `4.1.B` | 4 | 100 | Identify risks from algorithmic bias, biased collection practices, and incomplete or inaccurate data sets. | `missing (isolated)` | - | - | The seed has no topic for biased collection or incomplete and inaccurate data. A standalone data-quality topic has no evidenced tuple. | - |
| `4.1.C` | 4 | 100 | Evaluate whether a data set is fit to answer a specific problem without invalid extrapolation. | `missing (isolated)` | - | - | The seed has no topic for assessing whether a data set answers a question or supports extrapolation. A standalone fitness judgment has no evidenced tuple. | - |
| `4.2.A` | 4 | 101 | Represent everyday data-set workflows and tabular or charted structures in prose or diagrams prior to coding. | `missing (isolated)` | - | - | Arrays models a programming structure, with no topic for using everyday charts, tables, or diagrams to plan a data workflow. That planning topic has no evidenced tuple. | - |
| `4.3.A` | 4 | 102 | Create fixed-size 1D primitive or object arrays via new or initializer lists, index with [], check .length, and handle bounds errors. | `partial` | Arrays | - | Seed includes Arrays as a root component without Java array indexing, .length, or bounds errors. | - |
| `4.4.A` | 4 | 103 | Traverse 1D arrays with indexed for/while loops and enhanced for loops, distinguishing element-variable copies from object state updates. | `partial` | Arrays | - | Seed includes Arrays without Loops, Iteration, or enhanced-for traversal rules. | - |
| `4.5.A` | 4 | 104 | Implement and trace 1D array algorithms for extrema, totals, quantifiers, counts, adjacent pairs, duplicates, shifts or rotations, and reversal. | `partial` | Arrays | - | Seed includes Arrays without loop constructs or standard array algorithms. | - |
| `4.6.A` | 4 | 105-107 | Read persistent text files using java.io.File, Scanner, throws IOException, hasNext/next methods, literal String.split, and close. | `missing` | - | - | The seed has no file input or string processing topics for reading and splitting persistent text and handling IOException. | - |
| `4.7.A` | 4 | 108-109 | Use immutable Integer and Double wrappers with their primitives through boxing and unboxing. Use parseInt and parseDouble to parse strings into primitive numbers. | `missing` | - | - | The seed has no wrapper or parsing concepts for boxing Integer and Double values or converting text into primitives. | - |
| `4.8.A` | 4 | 110-111 | Create parameterized ArrayList&lt;E&gt; collections from java.util and trace size, add, get, set, and remove. | `missing` | - | - | Arrays provides a fixed-length collection concept, with no parameterized ArrayList or evidence of its insertion, access, and removal operations. | DSP-SEED-4.8.A-gen-poly (unresolved: out_of_scope_dependency) |
| `4.9.A` | 4 | 112 | Traverse ArrayList collections safely, handling index shifts on deletion, IndexOutOfBoundsException, and ConcurrentModificationException. | `missing` | - | - | The seed has no ArrayList traversal topic for coping with index shifts or collection modification exceptions. | - |
| `4.10.A` | 4 | 113 | Implement and trace ArrayList manipulation algorithms (including insertion and deletion) and parallel multi-collection traversals. | `missing` | - | - | The seed has no ArrayList algorithms for insertion, deletion, or processing multiple corresponding collections. | - |
| `4.11.A` | 4 | 114-115 | Represent rectangular 2D arrays as arrays of row arrays, index via [row][col], inspect row and column lengths, and handle bounds errors. | `missing` | - | - | Arrays has no edge or topic that identifies rectangular 2D arrays, row and column lengths, or row-column indexing. | - |
| `4.12.A` | 4 | 116 | Traverse 2D arrays in row-major, column-major, or custom orders using nested indexed or enhanced for loops. | `missing` | - | - | The seed has no 2D array or nested-loop topic for row-major, column-major, or custom traversals. | - |
| `4.13.A` | 4 | 117 | Implement and trace 2D array algorithms across full grids, specific rows or columns, or subsections. | `missing` | - | - | The seed has no topic for algorithms over 2D rows, columns, grids, or subregions. | - |
| `4.14.A` | 4 | 118 | Implement and trace linear search across 1D arrays, ArrayList instances, and row-by-row 2D arrays. | `missing` | - | - | Binary Search is the only search topic tied to Arrays. The seed has no sequential scan across arrays, ArrayList, or a 2D grid. | - |
| `4.15.A` | 4 | 119 | Trace each pass of iterative selection sort and insertion sort on arrays and ArrayList collections. | `missing` | - | - | Merge Sort is the only sorting topic in the seed. Selection and insertion sort passes over arrays or ArrayList are absent. | - |
| `4.16.A` | 4 | 120 | Trace recursive methods with base cases, per-call local variables, and iterative equivalence without authoring recursive code. | `partial` | Functions, Recursion | Recursion -[depends_on]-&gt; Functions | Seed models Recursion depending on Functions, but omits base cases, stack frames, iterative equivalence, and tracing-only scope. | - |
| `4.17.A` | 4 | 121 | Trace recursive traversals over strings, arrays, and ArrayList collections. | `partial` | Arrays, Recursion | - | Seed includes Arrays and Recursion across separate disconnected components, omitting Strings and ArrayList recursive traversals. | - |
| `4.17.B` | 4 | 121 | Trace each step of iterative or recursive binary search on sorted collections and compare efficiency to linear search. | `partial` | Arrays, Binary Search, Recursion | Binary Search -[depends_on]-&gt; Arrays | Seed connects Binary Search to Arrays in its Arrays component while Recursion sits in a separate disconnected component, omitting recursive binary search, ArrayList, and Linear Search comparison. | - |
| `4.17.C` | 4 | 121-122 | Trace recursive divide-and-merge steps of merge sort on arrays and ArrayList collections. | `partial` | Arrays, Merge Sort, Recursion | Merge Sort -[depends_on]-&gt; Recursion | Seed links Merge Sort to Recursion in the Algorithms component, omitting the prerequisite link to Arrays and omitting ArrayList sorting. | - |

## Unresolved Disputes

| Dispute ID | Objective | Kind | Edge | Rationale |
|---|---|---|---|---|
| `DSP-SEED-1.12.A-abs-isa` | `1.12.A` | `taxonomic_not_prerequisite` | `Abstract Classes -[is_a]-> Classes` | Seed includes Abstract Classes is_a Classes; runtime traverses is_a as a prerequisite even though Abstract Classes is out of AP CSA scope. |
| `DSP-SEED-1.12.A-cls-obj` | `1.12.A` | `disputed_prerequisite` | `Classes -[depends_on]-> Objects` | Seed treats Objects as prerequisite to Classes. |
| `DSP-SEED-1.12.A-inh` | `1.12.A` | `out_of_scope_dependency` | `Inheritance -[depends_on]-> Classes` | Seed links Inheritance to Classes, whereas CED 1.12.A.2 excludes designing inheritance. |
| `DSP-SEED-1.13.C-cls-obj` | `1.13.C` | `disputed_prerequisite` | `Classes -[depends_on]-> Objects` | Seed orders Objects before Classes. |
| `DSP-SEED-1.14.A-cls-obj` | `1.14.A` | `disputed_prerequisite` | `Classes -[depends_on]-> Objects` | Seed orders Objects before Classes. |
| `DSP-SEED-1.7.A-cls-obj` | `1.7.A` | `disputed_prerequisite` | `Classes -[depends_on]-> Objects` | Seed models Classes depending on Objects, reversing blueprint-to-instance conceptual dependency. |
| `DSP-SEED-3.1.A-abs-isa` | `3.1.A` | `taxonomic_not_prerequisite` | `Abstract Classes -[is_a]-> Classes` | Taxonomic Abstract Classes is_a Classes edge touches Classes. |
| `DSP-SEED-3.1.A-cls-obj` | `3.1.A` | `disputed_prerequisite` | `Classes -[depends_on]-> Objects` | Seed orders Objects before Classes. |
| `DSP-SEED-3.3.A-cls-obj` | `3.3.A` | `disputed_prerequisite` | `Classes -[depends_on]-> Objects` | Seed orders Objects before Classes. |
| `DSP-SEED-3.4.A-cls-obj` | `3.4.A` | `disputed_prerequisite` | `Classes -[depends_on]-> Objects` | Seed orders Objects before Classes. |
| `DSP-SEED-3.5.A-cls-obj` | `3.5.A` | `disputed_prerequisite` | `Classes -[depends_on]-> Objects` | Seed orders Objects before Classes. |
| `DSP-SEED-3.6.A-cls-obj` | `3.6.A` | `disputed_prerequisite` | `Classes -[depends_on]-> Objects` | Seed orders Objects before Classes. |
| `DSP-SEED-4.8.A-gen-poly` | `4.8.A` | `out_of_scope_dependency` | `Generics -[depends_on]-> Polymorphism` | Seed links Generics to Polymorphism rather than parameterized ArrayList&lt;E&gt; usage. |

## Concepts Outside Course Scope

| Concept | Category | Reason |
|---|---|---|
| `AVL Trees` | `beyond_course_scope` | Unit 4 collection objectives use 1D and 2D arrays and ArrayList. AVL tree balancing requires a separate tree representation and rotations. |
| `Abstract Classes` | `taxonomic_abstraction` | Unit 3 covers writing concrete Java classes and methods. The Abstract Classes is_a Classes tuple describes a subtype relationship, and abstract class design falls beyond those class-creation objectives. |
| `Binary Search Trees` | `beyond_course_scope` | CED 4.17.B traces binary search on an ordered collection. Binary search trees instead maintain ordering in linked tree nodes. |
| `Design Patterns` | `beyond_course_scope` | Unit 3 requires routine class design using fields, constructors, and methods. Named reusable object-oriented design patterns are outside that class-design scope. |
| `Dynamic Programming` | `beyond_course_scope` | Unit 4 traces traversal, search, sorting, and recursion on course collections. Dynamic programming adds caching and reuse of overlapping subproblems. |
| `Generics` | `excluded_by_ced` | Generic type authoring and polymorphism-bound generics are outside AP CSA (4.8.A uses parameterized ArrayList&lt;E&gt; only). |
| `Graph Traversal` | `beyond_course_scope` | Unit 4 traversal objectives use indexed data. Graph traversal requires following links between vertices. |
| `Graphs` | `beyond_course_scope` | Unit 4 uses 1D and 2D arrays and ArrayList. Vertex and edge structure is a separate data model. |
| `Hash Tables` | `beyond_course_scope` | Unit 4 specifies arrays and ArrayList as containers. Hash tables add key-based lookup and collision handling. |
| `Inheritance` | `excluded_by_ced` | CED 1.12.A.2 explicitly excludes designing and implementing inheritance hierarchies from Fall 2025 AP CSA. |
| `Interfaces` | `excluded_by_ced` | Unit 3 covers writing Java classes and methods. Interface declarations and implementation contracts fall beyond these class-creation objectives. |
| `Linked Lists` | `beyond_course_scope` | Unit 4 covers 1D and 2D arrays and ArrayList. Pointer-linked nodes and link traversal belong to a separate collection representation. |
| `Pointers` | `beyond_course_scope` | CED 2.6.B covers Java references and object equivalence. Explicit memory addresses and address arithmetic go beyond those references. |
| `Polymorphism` | `excluded_by_ced` | CED 1.12.A.2 excludes inheritance hierarchy design and implementation. Subtype polymorphism and dynamic dispatch are beyond the required uses of classes and objects. |
| `Trees` | `beyond_course_scope` | Unit 4 covers 1D and 2D arrays and ArrayList. Tree nodes and branches form a separate collection model. |
