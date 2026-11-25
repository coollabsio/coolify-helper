<?php

/**
 * Coolify's legacy watch paths, copied verbatim from app/Models/Application.php
 * (parseWatchPaths, isWatchPathsTriggered, matchPaths, globMatch, globToRegex)
 * as plain functions: Laravel collections are replaced by arrays, and the model
 * attribute by a parameter. The logic and the order of operations are unchanged.
 *
 * coolify-affected's Go port (internal/match/watchpaths.go) is tested against
 * this file. Do not "fix" anything here: it is the reference.
 */

/** Application::parseWatchPaths */
function watch_paths_parse($value)
{
    if ($value) {
        $watch_paths = array_map(function (string $path): string {
            // Trim whitespace
            $path = trim($path);

            if (str_starts_with($path, '!')) {
                $negation = '!';
                $pathWithoutNegation = substr($path, 1);
                $pathWithoutNegation = ltrim(trim($pathWithoutNegation), '/');

                return $negation.$pathWithoutNegation;
            }

            return ltrim($path, '/');
        }, explode("\n", $value));
        $watch_paths = array_filter($watch_paths, function (string $path): bool {
            return strlen($path) > 0;
        });

        return trim(implode("\n", $watch_paths));
    }
}

/** Laravel's blank() for a string or null. */
function watch_paths_blank(?string $value): bool
{
    if (is_null($value)) {
        return true;
    }

    return trim($value) === '';
}

/**
 * Application::isWatchPathsTriggered, for the stored value $watch_paths.
 * normalizeWatchPaths() re-parses the stored value before matching.
 */
function watch_paths_triggered(?string $watch_paths, array $modified_files): bool
{
    if (is_null($watch_paths)) {
        return false;
    }

    // normalizeWatchPaths(): the assignment goes through the watchPaths()
    // attribute's set mutator, which parses the value again (or stores null
    // for a falsy value).
    $normalized = watch_paths_parse($watch_paths);
    if ($normalized !== $watch_paths) {
        $watch_paths = $normalized ? watch_paths_parse($normalized) : null;
    }

    // explode() of a null gives [""] (with a deprecation notice).
    $watch_paths = explode("\n", (string) $watch_paths);

    if (count($watch_paths) === 0) {
        return false;
    }
    $matches = watch_paths_match_paths($modified_files, $watch_paths);

    return count($matches) > 0;
}

/**
 * Coolify's webhook decision for one application: deploy when the stored
 * watch paths are blank, or when a changed file is included.
 */
function watch_paths_should_deploy(?string $watch_paths, array $modified_files): bool
{
    if (watch_paths_blank($watch_paths)) {
        return true;
    }

    return watch_paths_triggered($watch_paths, $modified_files);
}

/** Application::matchPaths */
function watch_paths_match_paths(array $modified_files, ?array $watch_paths): array
{
    if (is_null($watch_paths) || count($watch_paths) === 0) {
        return [];
    }

    return array_values(array_filter($modified_files, function ($file) use ($watch_paths) {
        $shouldInclude = null; // null means no patterns matched

        // Process patterns in order - last match wins
        foreach ($watch_paths as $pattern) {
            $pattern = trim($pattern);
            if (empty($pattern)) {
                continue;
            }

            $isExclusion = str_starts_with($pattern, '!');
            $matchPattern = $isExclusion ? substr($pattern, 1) : $pattern;

            if (watch_paths_glob_match($matchPattern, $file)) {
                // This pattern matches - it determines the current state
                $shouldInclude = ! $isExclusion;
            }
        }

        // If no patterns matched and we only have exclusion patterns, include by default
        if ($shouldInclude === null) {
            // Check if we only have exclusion patterns
            $hasInclusionPatterns = false;
            foreach ($watch_paths as $p) {
                if (! str_starts_with(trim($p), '!')) {
                    $hasInclusionPatterns = true;
                    break;
                }
            }

            return ! $hasInclusionPatterns;
        }

        return $shouldInclude;
    }));
}

/** Application::globMatch */
function watch_paths_glob_match(string $pattern, string $path): bool
{
    $regex = watch_paths_glob_to_regex($pattern);

    return preg_match($regex, $path) === 1;
}

/** Application::globToRegex */
function watch_paths_glob_to_regex(string $pattern): string
{
    $regex = '';
    $inGroup = false;
    $chars = str_split($pattern);
    $len = count($chars);

    for ($i = 0; $i < $len; $i++) {
        $c = $chars[$i];

        switch ($c) {
            case '*':
                // Check for **
                if ($i + 1 < $len && $chars[$i + 1] === '*') {
                    // ** matches any number of directories
                    $regex .= '.*';
                    $i++; // Skip next *
                    // Skip optional /
                    if ($i + 1 < $len && $chars[$i + 1] === '/') {
                        $i++;
                    }
                } else {
                    // * matches anything except /
                    $regex .= '[^/]*';
                }
                break;

            case '?':
                // ? matches any single character except /
                $regex .= '[^/]';
                break;

            case '[':
                // Character class
                $inGroup = true;
                $regex .= '[';
                // Check for negation
                if ($i + 1 < $len && ($chars[$i + 1] === '!' || $chars[$i + 1] === '^')) {
                    $regex .= '^';
                    $i++;
                }
                break;

            case ']':
                if ($inGroup) {
                    $inGroup = false;
                    $regex .= ']';
                } else {
                    $regex .= preg_quote($c, '#');
                }
                break;

            case '.':
            case '(':
            case ')':
            case '+':
            case '{':
            case '}':
            case '$':
            case '^':
            case '|':
            case '\\':
                // Escape regex special characters
                $regex .= '\\'.$c;
                break;

            default:
                $regex .= $c;
                break;
        }
    }

    // Wrap in delimiters and anchors
    return '#^'.$regex.'$#';
}
