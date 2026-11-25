<?php

/**
 * Matcher for the compiled trigger set of coolify-affected (version 1).
 *
 * coolify-affected returns, per application, a compiled set:
 *
 *     {
 *       "version": 1,
 *       "all_of": [
 *         {"limit": "workspace", "default": "include", "rules": [
 *           {"action": "exclude", "kind": "dir",  "path": "apps/api"},
 *           {"action": "include", "kind": "file", "path": "README.md"},
 *           {"action": "include", "kind": "glob", "pattern": "docs/**"}
 *         ]}
 *       ],
 *       "lockfiles": ["pnpm-lock.yaml"]
 *     }
 *
 * A path matches the set when every ruleset of "all_of" includes it (an empty
 * "all_of" matches every path). In a ruleset, the last matching rule decides;
 * when no rule matches, "default" decides. Rule kinds:
 *
 *   - dir:  the path itself and every path under it ("a" matches "a" and "a/b",
 *           not "ab").
 *   - file: the exact path.
 *   - glob: "*" is any run of bytes except "/"; "**" as a whole segment is zero
 *           or more segments ("a/**" matches "a" and "a/b/c"); "?" is one
 *           character except "/" (one UTF-8 character, or one byte where the
 *           path is not valid UTF-8). Every other byte is literal: no classes,
 *           braces or escapes.
 *
 * Paths are repository-relative, without a leading "/", and compared byte for
 * byte, case-sensitively. "lockfiles" is not used here: a changed lockfile
 * that matches the set needs a full evaluation, because coolify-affected scopes
 * lockfile changes to the application's dependencies.
 *
 * The behaviour is pinned by testdata/matcher-conformance.json ("compiled").
 * This file depends on nothing but PHP 8.
 */

declare(strict_types=1);

/**
 * Throws an InvalidArgumentException unless $set is a valid compiled set of a
 * version this matcher understands.
 */
function compiled_set_validate(array $set): void
{
    if (($set['version'] ?? null) !== 1) {
        throw new InvalidArgumentException('unsupported compiled set version');
    }
    if (! is_array($set['all_of'] ?? [])) {
        throw new InvalidArgumentException('all_of must be a list');
    }
    foreach ($set['all_of'] ?? [] as $ruleset) {
        if (! in_array($ruleset['default'] ?? null, ['include', 'exclude'], true)) {
            throw new InvalidArgumentException('invalid ruleset default');
        }
        foreach ($ruleset['rules'] ?? [] as $rule) {
            if (! in_array($rule['action'] ?? null, ['include', 'exclude'], true)) {
                throw new InvalidArgumentException('invalid rule action');
            }
            $kind = $rule['kind'] ?? null;
            $value = $kind === 'glob' ? ($rule['pattern'] ?? null) : ($rule['path'] ?? null);
            if (! in_array($kind, ['dir', 'file', 'glob'], true) || ! is_string($value) || $value === '') {
                throw new InvalidArgumentException('invalid rule');
            }
        }
    }
}

/** Whether every ruleset of the set includes $path. */
function compiled_set_matches(array $set, string $path): bool
{
    foreach ($set['all_of'] ?? [] as $ruleset) {
        if (! compiled_ruleset_includes($ruleset, $path)) {
            return false;
        }
    }

    return true;
}

/** Whether a ruleset includes $path: the last matching rule decides, else the default. */
function compiled_ruleset_includes(array $ruleset, string $path): bool
{
    $action = $ruleset['default'];
    foreach ($ruleset['rules'] ?? [] as $rule) {
        if (compiled_rule_matches($rule, $path)) {
            $action = $rule['action'];
        }
    }

    return $action === 'include';
}

/** Whether one rule matches $path. */
function compiled_rule_matches(array $rule, string $path): bool
{
    switch ($rule['kind']) {
        case 'dir':
            return $path === $rule['path'] || str_starts_with($path, $rule['path'].'/');
        case 'file':
            return $path === $rule['path'];
        case 'glob':
            return compiled_glob_matches($rule['pattern'], $path);
    }
    throw new InvalidArgumentException('invalid rule kind');
}

/** Whether $path matches a glob (see the top of this file). */
function compiled_glob_matches(string $pattern, string $path): bool
{
    $memo = [];

    return compiled_glob_segments(explode('/', $pattern), 0, explode('/', $path), 0, $memo);
}

/**
 * Whether pattern segments from $pi match path segments from $si. "**" takes
 * zero or more segments; every other pattern segment takes exactly one. $memo
 * keeps it polynomial for patterns with many "**".
 */
function compiled_glob_segments(array $pattern, int $pi, array $path, int $si, array &$memo): bool
{
    $key = $pi.':'.$si;
    if (isset($memo[$key])) {
        return $memo[$key];
    }
    if ($pi === count($pattern)) {
        $result = $si === count($path);
    } elseif ($pattern[$pi] === '**') {
        $result = compiled_glob_segments($pattern, $pi + 1, $path, $si, $memo)
            || ($si < count($path) && compiled_glob_segments($pattern, $pi, $path, $si + 1, $memo));
    } else {
        $result = $si < count($path)
            && compiled_glob_segment($pattern[$pi], $path[$si])
            && compiled_glob_segments($pattern, $pi + 1, $path, $si + 1, $memo);
    }

    return $memo[$key] = $result;
}

/**
 * Whether one path segment matches one pattern segment ("*", "?" and literal
 * bytes). $reach[$j] is whether the pattern read so far matches the first $j
 * bytes of $name.
 */
function compiled_glob_segment(string $pattern, string $name): bool
{
    $n = strlen($name);
    $reach = array_fill(0, $n + 1, false);
    $reach[0] = true;
    for ($i = 0; $i < strlen($pattern); $i++) {
        $c = $pattern[$i];
        $next = array_fill(0, $n + 1, false);
        if ($c === '*') {
            $any = false;
            for ($j = 0; $j <= $n; $j++) {
                $any = $any || $reach[$j];
                $next[$j] = $any;
            }
        } elseif ($c === '?') {
            for ($j = 0; $j < $n; $j++) {
                if ($reach[$j]) {
                    $next[$j + compiled_utf8_char_length($name, $j)] = true;
                }
            }
        } else {
            for ($j = 0; $j < $n; $j++) {
                if ($reach[$j] && $name[$j] === $c) {
                    $next[$j + 1] = true;
                }
            }
        }
        $reach = $next;
    }

    return $reach[$n];
}

/**
 * The length in bytes of the character at $s[$i]: the length of a valid UTF-8
 * sequence there, else 1 (an invalid or truncated sequence is one byte).
 */
function compiled_utf8_char_length(string $s, int $i): int
{
    $b = ord($s[$i]);
    if ($b < 0x80) {
        return 1;
    }
    // [length, lowest second byte, highest second byte] per lead byte: this
    // excludes overlong forms, UTF-16 surrogates and code points above U+10FFFF.
    if ($b >= 0xC2 && $b <= 0xDF) {
        [$length, $low, $high] = [2, 0x80, 0xBF];
    } elseif ($b === 0xE0) {
        [$length, $low, $high] = [3, 0xA0, 0xBF];
    } elseif ($b === 0xED) {
        [$length, $low, $high] = [3, 0x80, 0x9F];
    } elseif ($b >= 0xE1 && $b <= 0xEF) {
        [$length, $low, $high] = [3, 0x80, 0xBF];
    } elseif ($b === 0xF0) {
        [$length, $low, $high] = [4, 0x90, 0xBF];
    } elseif ($b >= 0xF1 && $b <= 0xF3) {
        [$length, $low, $high] = [4, 0x80, 0xBF];
    } elseif ($b === 0xF4) {
        [$length, $low, $high] = [4, 0x80, 0x8F];
    } else {
        return 1;
    }
    if ($i + $length > strlen($s)) {
        return 1;
    }
    $second = ord($s[$i + 1]);
    if ($second < $low || $second > $high) {
        return 1;
    }
    for ($k = 2; $k < $length; $k++) {
        $continuation = ord($s[$i + $k]);
        if ($continuation < 0x80 || $continuation > 0xBF) {
            return 1;
        }
    }

    return $length;
}
