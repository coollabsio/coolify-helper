package match

// conformanceCases builds the cases of testdata/matcher-conformance.json. The
// compiled expectations are written by hand; -update checks them against the
// Go and PHP matchers. Watch paths expectations come from the PHP reference;
// the ones written here (not nil) are checked against it.
func conformanceCases() builtConformance {
	const T, F = true, false
	y, n := new(true), new(false)
	var b builtConformance
	b.Version = 1

	inc := func(rules ...Rule) Set { return set(ruleset("build_context", Exclude, rules...)) }
	exc := func(rules ...Rule) Set { return set(ruleset("workspace", Include, rules...)) }

	// Defaults and empty sets.
	b.compiled("empty all_of", set(), map[string]bool{
		"a": T, "README.md": T, "apps/web/index.ts": T, "é": T, "a b/c": T,
	})
	b.compiled("default include without rules", set(ruleset("workspace", Include)), map[string]bool{
		"x": T, "a/b": T, "apps/web": T,
	})
	b.compiled("default exclude without rules", set(ruleset("dockerfile", Exclude)), map[string]bool{
		"x": F, "a/b": F, "Dockerfile": F,
	})

	// dir rules.
	b.compiled("dir apps/web", inc(IncludeDir("apps/web", nil)), map[string]bool{
		"apps/web": T, "apps/web/index.ts": T, "apps/web/src/a/b/c.tsx": T, "apps/web/.env": T,
		"apps/web2/index.ts": F, "apps/web2": F, "apps/webx": F, "apps/we": F, "apps": F,
		"apps/api/index.ts": F, "Apps/web/index.ts": F, "apps/Web/index.ts": F,
		"x/apps/web/y": F, "apps/web.ts": F, "apps/web-old/x": F,
	})
	b.compiled("dir vs dir2 prefix trap", exc(ExcludeDir("dir", nil)), map[string]bool{
		"dir": F, "dir/a": F, "dir/dir2/a": F, "dir2/a": T, "dir2": T, "dir.txt": T,
		"dirx/a": T, "di": T, "a/dir/b": T,
	})
	b.compiled("nested dir overrides", inc(
		IncludeDir("packages", nil),
		ExcludeDir("packages/internal", nil),
		IncludeDir("packages/internal/public", nil),
	), map[string]bool{
		"packages": T, "packages/a/x": T, "packages/internal": F, "packages/internal/x": F,
		"packages/internal/public": T, "packages/internal/public/y": T,
		"packages/internal/publicity/y": F, "packages/internals/x": T, "pkg/x": F,
	})
	b.compiled("deep dir", inc(IncludeDir("a/b/c/d/e", nil)), map[string]bool{
		"a/b/c/d/e": T, "a/b/c/d/e/f/g": T, "a/b/c/d": F, "a/b/c/d/ef": F, "a/b/c/d/e.txt": F,
	})
	b.compiled("submodule gitlinks", set(
		ruleset("workspace", Include, ExcludeDir("vendor/lib", nil)),
		ruleset("build_context", Exclude, IncludeDir("apps/x", nil), IncludeDir("vendor", nil)),
	), map[string]bool{
		"apps/x": T, "apps/x/sub": T, "apps/y": F, "vendor/lib": F, "vendor/other": T, "vendor/lib2": T,
	})

	// file rules.
	b.compiled("file package.json", inc(IncludeFile("package.json", nil)), map[string]bool{
		"package.json": T, "Package.json": F, "package.json5": F, "package.jso": F,
		"a/package.json": F, "package.json/x": F, "package.json.bak": F, " package.json": F,
	})
	b.compiled("file in a directory", inc(IncludeFile("apps/web/Dockerfile", nil)), map[string]bool{
		"apps/web/Dockerfile": T, "apps/web/Dockerfile.dev": F, "apps/web": F,
		"apps/web/dockerfile": F, "apps/web/Dockerfile/x": F, "Dockerfile": F,
	})
	b.compiled("file does not include below it", inc(IncludeFile("apps/x", nil)), map[string]bool{
		"apps/x": T, "apps/x/y": F, "apps/xy": F,
	})
	b.compiled("dot names", inc(IncludeFile("..a", nil), IncludeDir("...", nil)), map[string]bool{
		"..a": T, "a": F, ".../x": T, "...": T, "..../x": F,
	})

	// Last matching rule wins.
	b.compiled("exclude after include", inc(IncludeDir("apps", nil), ExcludeDir("apps/api", nil)), map[string]bool{
		"apps/web/x": T, "apps/api/x": F, "apps/api": F, "apps/apix/y": T, "apps": T,
	})
	b.compiled("include after exclude", inc(ExcludeDir("apps/api", nil), IncludeDir("apps", nil)), map[string]bool{
		"apps/api/x": T, "apps/web/x": T, "lib/x": F,
	})
	b.compiled("re-include a file", exc(ExcludeDir("docs", nil), IncludeFile("docs/api.yaml", nil)), map[string]bool{
		"docs/api.yaml": T, "docs/guide.md": F, "docs": F, "docs/api.yaml/x": F, "src/x": T,
	})
	b.compiled("same path repeated", exc(
		ExcludeDir("a", nil), IncludeDir("a", nil),
		ExcludeFile("f", nil), IncludeFile("f", nil),
		IncludeFile("g", nil), ExcludeFile("g", nil),
	), map[string]bool{
		"a/x": T, "a": T, "f": T, "g": F, "h": T,
	})
	b.compiled("glob then dir", exc(IncludeGlob("**/*.ts", nil), ExcludeDir("packages/b", nil)), map[string]bool{
		"packages/b/x.ts": F, "packages/a/x.ts": T, "packages/b/x.md": F, "x.ts": T,
	})
	b.compiled("dir then glob then file", inc(
		IncludeDir("docs", nil), ExcludeGlob("**/*.md", nil), IncludeFile("docs/README.md", nil),
	), map[string]bool{
		"docs/a.md": F, "docs/README.md": T, "docs/x.txt": T, "docs/sub/b.md": F,
		"README.md": F, "docs.md": F, "docs/sub/README.md": F,
	})
	b.compiled("globs in order", inc(
		IncludeGlob("**", nil), ExcludeGlob("**/*.md", nil), IncludeGlob("docs/**", nil),
	), map[string]bool{
		"x.ts": T, "x.md": F, "docs/x.md": T, "docs": T, "a/docs/x.md": F,
	})
	b.compiled("rules that agree with the default", exc(IncludeDir("a", nil), IncludeGlob("*.md", nil)), map[string]bool{
		"a/x": T, "x.md": T, "b": T,
	})

	// Globs.
	b.compiled("glob packages/*/package.json", inc(IncludeGlob("packages/*/package.json", nil)), map[string]bool{
		"packages/a/package.json": T, "packages/package.json": F, "packages/a/b/package.json": F,
		"packages/.hidden/package.json": T, "packages/a b/package.json": T,
		"packages/a/package.json5": F, "Packages/a/package.json": F,
	})
	b.compiled("glob ** at the start", inc(IncludeGlob("**/*.md", nil)), map[string]bool{
		"README.md": T, "docs/README.md": T, "a/b/c/d.md": T, "README.mdx": F,
		".md": T, "docs/.md": T, "md": F, "a.md/b": F,
	})
	b.compiled("glob ** at the end", inc(IncludeGlob("docs/**", nil)), map[string]bool{
		"docs": T, "docs/a": T, "docs/a/b/c": T, "docsx/a": F, "doc": F, "x/docs/a": F,
	})
	b.compiled("glob ** in the middle", inc(IncludeGlob("src/**/test/*.ts", nil)), map[string]bool{
		"src/test/a.ts": T, "src/a/test/b.ts": T, "src/a/b/test/c.ts": T, "src/test/a/b.ts": F,
		"src/test.ts": F, "src/atest/b.ts": F, "x/src/test/a.ts": F,
	})
	b.compiled("glob ** alone", inc(IncludeGlob("**", nil)), map[string]bool{
		"a": T, "a/b/c": T, ".x": T,
	})
	b.compiled("glob * alone", inc(IncludeGlob("*", nil)), map[string]bool{
		"a": T, ".env": T, "a/b": F,
	})
	b.compiled("glob a/*", inc(IncludeGlob("a/*", nil)), map[string]bool{
		"a/b": T, "a": F, "a/b/c": F, "b/a": F,
	})
	b.compiled("glob **/**/x", inc(IncludeGlob("**/**/x", nil)), map[string]bool{
		"x": T, "a/x": T, "a/b/x": T, "a/xx": F,
	})
	b.compiled("glob **/node_modules/**", exc(ExcludeGlob("**/node_modules/**", nil)), map[string]bool{
		"node_modules": F, "node_modules/x": F, "a/node_modules/b/c": F,
		"node_modules2/x": T, "xnode_modules/a": T, "a/b": T,
	})
	b.compiled("glob **/name without /**", exc(ExcludeGlob("**/node_modules", nil)), map[string]bool{
		"node_modules": F, "a/node_modules": F, "node_modules/x": T,
	})
	b.compiled("glob a**b is not a whole segment", inc(IncludeGlob("a**b", nil)), map[string]bool{
		"ab": T, "aXb": T, "a/b": F, "aX/Yb": F,
	})
	b.compiled("glob **.js", inc(IncludeGlob("**.js", nil)), map[string]bool{
		"a.js": T, "a/b.js": F, ".js": T,
	})
	b.compiled("glob a/**b", inc(IncludeGlob("a/**b", nil)), map[string]bool{
		"a/b": T, "a/xb": T, "a/x/b": F, "a": F,
	})
	b.compiled("glob ***", inc(IncludeGlob("***", nil)), map[string]bool{
		"abc": T, "a/b": F,
	})
	b.compiled("glob x/***/y", inc(IncludeGlob("x/***/y", nil)), map[string]bool{
		"x/y": F, "x/z/y": T, "x/z/w/y": F,
	})
	b.compiled("glob *a*b*c*", inc(IncludeGlob("*a*b*c*", nil)), map[string]bool{
		"abc": T, "xaxbxcx": T, "acb": F, "a/bc": F, "aabbcc": T,
	})
	b.compiled("glob a?c", inc(IncludeGlob("a?c", nil)), map[string]bool{
		"abc": T, "ac": F, "abbc": F, "a/c": F, "a.c": T, "aéc": T, "a😀c": T, "ác": T,
	})
	b.compiled("glob ?/?", inc(IncludeGlob("?/?", nil)), map[string]bool{
		"a/b": T, "ab/c": F, "é/😀": T, "a": F,
	})
	b.compiled("glob caf? (NFC vs NFD)", inc(IncludeGlob("caf?", nil)), map[string]bool{
		"café": T, "café": F, "caf": F, "cafés": F,
	})
	b.compiled("glob caf?? (NFC vs NFD)", inc(IncludeGlob("caf??", nil)), map[string]bool{
		"café": F, "café": T,
	})
	b.compiled("glob .*", inc(IncludeGlob(".*", nil)), map[string]bool{
		".env": T, ".github": T, "a.b": F, ".a/b": F,
	})
	b.compiled("glob **/.env*", inc(IncludeGlob("**/.env*", nil)), map[string]bool{
		".env": T, "a/.env.local": T, "a/b.env": F, ".envrc": T,
	})

	b.compiled("glob *.?s", inc(IncludeGlob("*.?s", nil)), map[string]bool{
		"a.js": T, "a.ts": T, "a.tsx": F, "a.s": F, "b/a.js": F, ".js": T, "a.és": T,
	})
	b.compiled("glob **/Dockerfile*", inc(IncludeGlob("**/Dockerfile*", nil)), map[string]bool{
		"Dockerfile": T, "a/Dockerfile.dev": T, "a/dockerfile": F, "Dockerfiles/x": F, "a/b/Dockerfile": T,
	})
	b.compiled("many sibling dirs", set(ruleset("workspace", Include,
		ExcludeDir("packages/a", nil), ExcludeDir("packages/b", nil), ExcludeDir("packages/c", nil),
		ExcludeDir("packages/d", nil), ExcludeDir("apps/api", nil), ExcludeDir("apps/docs", nil),
	)), map[string]bool{
		"packages/a/x": F, "packages/c/y/z": F, "packages/e/x": T, "packages/ab/x": T,
		"apps/web/x": T, "apps/docs": F, "packages": T,
	})

	// Tricky characters: all literal, byte-exact.
	b.compiled("unicode NFC dir", inc(IncludeDir("café", nil)), map[string]bool{
		"café/x": T, "café/x": F, "Café/x": F,
	})
	b.compiled("unicode NFD dir", inc(IncludeDir("café", nil)), map[string]bool{
		"café/x": T, "café/x": F,
	})
	b.compiled("unicode paths", inc(IncludeGlob("文档/**", nil), IncludeFile("😀.txt", nil)), map[string]bool{
		"文档/a.md": T, "文档": T, "文/a": F, "😀.txt": T, "😀": F,
	})
	b.compiled("spaces", inc(IncludeDir("my app", nil), IncludeFile("a b/c d.txt", nil), IncludeGlob("* *", nil)), map[string]bool{
		"my app": T, "my app/x": T, "my  app/x": F, "myapp/x": F, "a b/c d.txt": T,
		"a b/c  d.txt": F, "x y": T, "xy": F, " ": T, " /x": F,
	})
	b.compiled("dots", inc(IncludeDir(".github", nil), IncludeFile(".env", nil)), map[string]bool{
		".github/workflows/ci.yml": T, ".github": T, "github/x": F, ".env": T,
		".env.local": F, "x/.env": F, "..env": F,
	})
	b.compiled("brackets are literal", inc(IncludeGlob("pages/[id]/*.tsx", nil), IncludeFile("[slug].tsx", nil)), map[string]bool{
		"pages/[id]/index.tsx": T, "pages/i/index.tsx": F, "pages/d/index.tsx": F,
		"[slug].tsx": T, "s.tsx": F,
	})
	b.compiled("bracket glob is not a class", inc(IncludeGlob("[ab]", nil), IncludeGlob("x]", nil), IncludeGlob("[!a]", nil)), map[string]bool{
		"a": F, "[ab]": T, "x]": T, "[!a]": T, "b": F,
	})
	b.compiled("braces are literal", inc(IncludeGlob("*.{js,ts}", nil), IncludeDir("{a,b}", nil)), map[string]bool{
		"a.js": F, "a.{js,ts}": T, "{a,b}/x": T, "a/x": F,
	})
	b.compiled("hash", inc(IncludeDir("c#", nil), IncludeFile("notes#1.md", nil), IncludeGlob("#*", nil)), map[string]bool{
		"c#/x.cs": T, "c/x": F, "notes#1.md": T, "notes": F, "#readme": T, "a#": F,
	})
	b.compiled("backslash is literal", inc(IncludeGlob(`a\*`, nil), IncludeFile(`win\path`, nil)), map[string]bool{
		`a\*`: T, `a\x`: T, "a*": F, "ax": F, `win\path`: T, "win/path": F,
	})
	b.compiled("case-sensitive", inc(IncludeDir("Apps", nil), IncludeGlob("*.MD", nil)), map[string]bool{
		"Apps/x": T, "apps/x": F, "APPS/x": F, "README.MD": T, "README.md": F,
	})
	b.compiled("regex characters are literal", inc(IncludeGlob("a.b+c(d)|e$^", nil)), map[string]bool{
		"a.b+c(d)|e$^": T, "axb+c(d)|e$^": F, "abbc(d)|e$^": F,
	})

	// all_of.
	b.compiled("all_of two rulesets", set(
		ruleset("workspace", Include, ExcludeDir("apps/api", nil), ExcludeDir("docs", nil)),
		ruleset("dockerfile", Exclude, IncludeDir("apps", nil), IncludeFile("package.json", nil), IncludeFile("pnpm-lock.yaml", nil)),
	), map[string]bool{
		"apps/web/x": T, "apps/api/x": F, "package.json": T, "docs/x": F, "lib/x": F, "pnpm-lock.yaml": T,
	})
	b.compiled("all_of three rulesets", set(
		ruleset("workspace", Include, ExcludeDir("apps/api", nil)),
		ruleset("dockerfile", Exclude, IncludeDir("apps", nil), IncludeFile("package.json", nil)),
		ruleset("build_context", Include, ExcludeGlob("**/*.md", nil)),
	), map[string]bool{
		"apps/web/README.md": F, "apps/web/x.ts": T, "package.json": T, "apps/api/x.ts": F, "README.md": F,
	})
	b.compiled("all_of with an excluding ruleset", set(ruleset("workspace", Include), ruleset("compose", Exclude)), map[string]bool{
		"x": F, "a/b": F,
	})

	// Realistic sets.
	b.compiled("pnpm workspace app", set(ruleset("workspace", Exclude,
		IncludeDir("apps/web", nil),
		IncludeDir("packages/ui", nil),
		IncludeFile("package.json", nil),
		IncludeFile("pnpm-workspace.yaml", nil),
		IncludeFile("pnpm-lock.yaml", nil),
		IncludeGlob("turbo.json", nil),
		ExcludeGlob("**/*.test.ts", nil),
		ExcludeDir("apps/web/e2e", nil),
	)), map[string]bool{
		"apps/web/src/page.tsx": T, "apps/web/src/page.test.ts": F, "apps/web/e2e/login.spec.ts": F,
		"packages/ui/button.tsx": T, "packages/ui": T, "packages/utils/index.ts": F, "package.json": T,
		"README.md": F, "turbo.json": T, "apps/api/index.ts": F,
	})
	b.compiled("dockerignore build context", set(ruleset("build_context", Include,
		ExcludeGlob("**/node_modules/**", nil),
		ExcludeGlob("**/*.log", nil),
		ExcludeDir(".git", nil),
		IncludeGlob("important.log", nil),
	)), map[string]bool{
		"node_modules/x": F, "a/node_modules/b/c": F, "node_modules": F, "node_modules2/x": T,
		"x.log": F, "a/x.log": F, "important.log": T, "a/important.log": F, ".git/config": F,
		".gitignore": T, "src/x.ts": T,
	})

	// Watch paths: Coolify's unit tests, as stored values.
	type w = map[string]*bool
	b.watch("exact", "docker-compose.yml\nDockerfile", w{
		"docker-compose.yml": y, "Dockerfile": y, "README.md": n, "dockerfile": n, "a/Dockerfile": n,
	})
	b.watch("wildcards", "*.yml\nsrc/**/*.php\nconfig/*", w{
		"docker-compose.yml": y, "production.yml": y, "src/Controllers/UserController.php": y,
		"src/Models/User.php": y, "config/app.php": y, "README.md": n, "src/index.js": n,
		"configurations/deep/file.php": n, "src/a.php": y, "config/a/b": n, "a/b.yml": n,
	})
	b.watch("leading slash include", "/docs/**", w{"docs/index.md": y, "docs": n})
	b.watch("leading slash negation", "!/docs/**", w{"docs/index.md": n, "src/app.ts": y})
	b.watch("js and ts", "**/*.js\n**/*.jsx\n**/*.ts\n**/*.tsx", w{
		"src/index.js": y, "index.js": y, "components/Button.jsx": y, "src/components/ui/Button.tsx": y,
		"README.md": n, "package.json": n,
	})
	b.watch("question mark", "test?.txt\nfile-?.yml", w{
		"test1.txt": y, "testA.txt": y, "test.txt": n, "test12.txt": n, "testé.txt": n, "file-B.yml": y, "file.yml": n,
	})
	b.watch("character sets", "[abc]test.txt\nfile[0-9].yml", w{
		"atest.txt": y, "ctest.txt": y, "dtest.txt": n, "test.txt": n, "file9.yml": y, "fileA.yml": n,
	})
	b.watch("docker compose", "docker-compose*.yml\n.env*\nDockerfile*\nservices/**", w{
		"docker-compose.prod.yml": y, "docker-compose-dev.yml": y, ".env": y, ".env.local": y,
		"Dockerfile.prod": y, "services/api/app.js": y, "services": n, "config/nginx.conf": n, "x/.env": n,
	})
	b.watch("negation only", "!docker-compose-test/**", w{
		"docker-compose/index.ts": y, "docker-compose-test/index.ts": n,
		"docker-compose-test/sub/dir/file.ts": n, "docker-compose-test": y,
	})
	b.watch("mixed", "**/*.js\n!**/*test*/**", w{
		"src/index.js": y, "test/unit/app.js": n, "src/test-utils/helper.js": n,
		"docker-compose-test/index.js": n, "README.md": n, "testing.js": y,
	})
	b.watch("multiple negations", "!tests/**\n!docs/**\n!*.md", w{
		"src/index.js": y, "tests/unit/test.js": n, "README.md": n, "docs/README.md": n, "src/README.md": y,
	})
	b.watch("exclude then include", "!docker-compose/**\ndocker-compose/**", w{"docker-compose/index.ts": y})
	b.watch("include then exclude", "docker-compose/**\n!docker-compose/**", w{"docker-compose/index.ts": n})
	b.watch("overlapping", "**/*.js\n!**/test/**\nsrc/test/unit.js", w{
		"src/test/unit.js": y, "src/components/Button.js": y, "test/integration.js": n,
	})
	b.watch("specific overrides", "!docs/**\ndocs/internal/**\nsrc/**", w{
		"docs/api.md": n, "docs/internal/secret.md": y, "src/index.js": y, "other.txt": n,
	})
	b.watch("precedence include wins", "**/*.json\n!app/**\napp/*.json", w{"app/config.json": y, "app/x/config.json": n})
	b.watch("precedence exclude wins", "**/*.json\napp/*.json\n!app/**", w{"app/config.json": n, "config.json": y})

	// Watch paths: "**" and "*".
	b.watch("dir/** needs the slash", "apps/**", w{"apps": n, "apps/x": y, "appsx": n})
	b.watch("a/**/b consumes the slash", "a/**/b", w{"a/b": y, "a/x/b": y, "a/xb": y, "ab": n})
	b.watch("a**b crosses slashes", "a**b", w{"a/x/b": y, "ab": y, "a\nb": n})
	b.watch("**.js", "**.js", w{"a/b.js": y, "b.js": y})
	b.watch("** alone", "**", w{"anything/at/all": y, "x": y})
	b.watch("* alone", "*", w{"a": y, "a/b": n})
	b.watch("*/", "*/", w{"a/": y, "a": n})
	b.watch("**/node_modules/**", "**/node_modules/**", w{"node_modules/x": y, "a/node_modules/x": y, "xnode_modules/a": y, "node_modules": n})
	b.watch("**/*.ts", "**/*.ts", w{"a.ts": y, "a/b/c.ts": y, "a.tsx": n})
	b.watch("src/**/*.php", "src/**/*.php", w{"src/a.php": y, "srcx/a.php": n, "src/a/b/c.php": y})
	b.watch("* crosses a newline", "a*b", w{"a\nb": y, "a/b": n})
	b.watch("? matches a newline", "a?b", w{"a\nb": y, "a/b": n})

	// Watch paths: PCRE quirks.
	b.watch("$ matches before a final newline", "a", w{"a\n": y, "a\nb": n, "a\r": n, "a": y, "A": n})
	b.watch("? is one byte", "caf?", w{"café": n, "cafe": y})
	b.watch("?? is two bytes", "caf??", w{"café": y})
	b.watch("a class of bytes", "caf[é]", w{"café": n, "caf": n})
	b.watch("nfc vs nfd", "café", w{"café": y, "café": n})
	b.watch("# breaks the pattern", "c#/**", w{"c#/x": n, "c/x": n})
	b.watch("# still counts as an inclusion", "c#/**\n!docs/**", w{"src/x": n, "docs/x": n})
	b.watch("# alone", "#", w{"#": n})
	b.watch("# in an exclusion", "!#x", w{"a": y, "#x": y})
	b.watch("dot is literal", "a.b", w{"axb": n, "a.b": y})
	b.watch("plus is literal", "a+", w{"a+": y, "aa": n})
	b.watch("braces are literal", "x{1,2}", w{"x{1,2}": y, "x": n, "xx": n})
	b.watch("pipe is literal", "a|b", w{"a": n, "a|b": y})
	b.watch("caret is literal", "^a", w{"^a": y, "a": n})
	b.watch("dollar is literal", "a$", w{"a$": y, "a": n})
	b.watch("parentheses are literal", "(a)", w{"(a)": y, "a": n})
	b.watch("backslash is literal", `a\b`, w{`a\b`: y, "ab": n})
	b.watch("trailing backslash", `a\`, w{`a\`: y})
	b.watch("lone ] is literal", "a]", w{"a]": y})
	b.watch("unterminated class", "[ab", w{"[ab": n, "a": n})
	b.watch("unterminated class in a path", "src/[", w{"src/[": n})
	b.watch("reversed range", "[z-a]", w{"m": n})
	b.watch("range", "[a-z]", w{"m": y, "M": n})
	b.watch("] first in a class", "[]?", w{"]": y, "[": y, "/": y, "a": n})
	b.watch("] first in a negated class", "[^]?", w{"b": y, "]": n})
	b.watch("[]a] is unterminated", "[]a]", w{"]": n, "a": n})
	b.watch("[!] is unterminated", "[!]", w{"x": n, "]": n})
	b.watch("negated class", "[!a]x", w{"bx": y, "ax": n, "/x": y})
	b.watch("negated class with ^", "[^a]x", w{"bx": y, "ax": n})
	b.watch("class swallows ?", "[a?]", w{"/]": y, "a]": y, "a": n})
	b.watch("class swallows *", "[a*]", w{"a]": y, "]": y, "aa^/]": y, "a": n})
	b.watch("class with [", "[[]", w{"[": y, "]": n})
	b.watch("range of escaped bytes", "[.-0]x", w{"/x": y, "-x": n})
	b.watch("dash after a range", "[a-c-e]", w{"-": nil, "d": nil, "e": nil, "b": y})
	b.watch("dash first", "[-a]", w{"-": y, "a": y, "b": n})
	b.watch("dash last", "[a-]", w{"-": y, "a": y})
	b.watch("range ending in a dash", "[%--]", w{"+": y, ".": n})

	// Watch paths: normalization.
	b.watch("line 0 is skipped", "a\n0", w{"0": n, "a": y})
	b.watch("line 0 counts as an inclusion", "!a\n0", w{"b": n, "a": n})
	b.watch("value 0", "0", w{"0": n, "x": n})
	b.watch("value /0", "/0", w{"0": n})
	b.watch("! alone", "!", w{"x": y})
	b.watch("!! excludes !", "!!x", w{"!x": n, "x": y})
	b.watch("CRLF", "a\r\nb\r\n", w{"b": y, "a": y, "c": n})
	b.watch("leading slashes", "///docs/**", w{"docs/x": y})
	b.watch("negation spaces and slashes", "! //docs/**", w{"docs/x": n, "src/x": y})
	b.watch("case-sensitive", "Docs/**", w{"docs/x": n, "Docs/x": y})
	b.watch("blank", "  ", w{"x": y})
	b.watch("empty", "", w{"x": y})
	b.watch("blank lines", "\n\n\t\n", w{"x": y})
	b.watch("slash only", "/", w{"x": n})
	b.watch("normalized twice", "/ /x", w{"x": y, "/x": n})
	b.watch("not normalized three times", "/ / /x", w{"x": n, "/x": y})
	b.watch("tabs and spaces", "\tsrc/**  ", w{"src/a": y})
	b.watch("empty lines", "src/**\n\n\n", w{"src/a": y})
	b.watch("tab inside", "a\tb", w{"a\tb": y})
	b.watch("NUL inside", "a\x00b", w{"a\x00b": nil, "ab": n})
	b.watch("form feed is not trimmed", "\fa", w{"a": n, "\fa": y})
	b.watch("exclusion after inclusion", "*.md\n!README.md", w{"README.md": n, "CHANGELOG.md": y, "docs/a.md": n})
	b.watch("inclusion after exclusion", "!*.md\nREADME.md", w{"README.md": y, "x.txt": n})
	b.watch("include all, exclude all", "*\n!*", w{"a": n})
	b.watch("exclude all, include all", "!*\n*", w{"a": y})
	b.watch("packages", "packages/*/package.json", w{"packages/a/package.json": y, "packages/a/b/package.json": n})
	b.watch("spaces", "my app/**", w{"my app/x": y, "my  app/x": n})
	b.watch("unicode", "文档/**", w{"文档/a": y, "文档": n})
	b.watch("? and unicode", "?", w{"é": n, "a": y})
	return b
}
