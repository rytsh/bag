package base

// BuiltinGlobals are well-known runtime/library names that must never be
// resolved as project-defined call targets (ported from Graphify's
// _LANGUAGE_BUILTIN_GLOBALS).
var BuiltinGlobals = NewSet(
	"String", "Number", "Boolean", "Object", "Array", "Symbol", "BigInt", "Date", "RegExp",
	"Error", "TypeError", "RangeError", "SyntaxError", "ReferenceError", "EvalError",
	"URIError", "Promise", "Map", "Set", "WeakMap", "WeakSet", "JSON", "Math", "Reflect",
	"Proxy", "Intl", "parseInt", "parseFloat", "isNaN", "isFinite", "encodeURIComponent",
	"decodeURIComponent", "encodeURI", "decodeURI", "URL", "URLSearchParams", "FormData",
	"Blob", "File", "Headers", "Request", "Response", "AbortController", "AbortSignal",
	"TextEncoder", "TextDecoder", "console", "str", "int", "float", "bool", "list", "dict",
	"set", "tuple", "bytes", "len", "range", "enumerate", "zip", "map", "filter", "sum",
	"min", "max", "print", "open", "isinstance", "type", "super", "sorted", "reversed",
	"any", "all", "abs", "round", "next", "iter", "hash", "id", "repr", "callable",
	"getattr", "setattr", "hasattr", "delattr", "vars", "dir", "Int", "Int8", "Int16",
	"Int32", "Int64", "UInt", "UInt8", "UInt16", "UInt32", "UInt64", "Double", "Float",
	"Bool", "Character", "Sendable", "Codable", "Decodable", "Encodable", "Equatable",
	"Hashable", "Identifiable", "Comparable", "CaseIterable", "RawRepresentable",
	"CustomStringConvertible", "CustomDebugStringConvertible", "AnyObject",
	"LocalizedError", "Data", "UUID", "Decimal", "Calendar", "Locale", "TimeZone", "Bundle",
	"IndexPath", "IndexSet", "NotificationCenter", "UserDefaults", "FileManager",
	"URLSession", "URLRequest", "URLComponents", "JSONDecoder", "JSONEncoder",
	"DateFormatter", "NumberFormatter", "ISO8601DateFormatter", "NSObject", "NSString",
	"NSError", "NSLock", "NSAttributedString", "DispatchQueue", "DispatchGroup",
	"OperationQueue", "RunLoop", "View", "Color", "Font",
)
