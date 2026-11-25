<?php

/**
 * Test harness for the PHP reference matchers. It reads one JSON request on
 * stdin and writes one JSON response on stdout. Every string that may hold
 * arbitrary bytes is base64-encoded, because JSON strings cannot.
 *
 *   {"mode": "watch_paths", "cases": [{"watch_paths": b64, "file": b64}]}
 *     → {"results": [{"blank": bool, "triggered": bool, "deploy": bool}]}
 *
 *   {"mode": "compiled", "cases": [{"compiled": set, "file": b64}]}
 *     (every rule "path" and "pattern" of the set is b64 too)
 *     → {"results": [{"match": bool}]}
 *
 * Used by internal/match's tests: php testdata/php/run.php < request.json
 */

declare(strict_types=1);

require __DIR__.'/watch_paths.php';
require __DIR__.'/compiled_matcher.php';

// preg_match() warns when a watch path makes an invalid pattern, and returns
// false. Under Laravel the warning is an exception; the reference (and Go)
// treat it as "the pattern does not match". Keep stdout clean JSON.
set_error_handler(fn () => true);

$request = json_decode(stream_get_contents(STDIN), true, 512, JSON_THROW_ON_ERROR);
$results = [];

foreach ($request['cases'] as $case) {
    $file = base64_decode($case['file'], true);
    switch ($request['mode']) {
        case 'watch_paths':
            $value = base64_decode($case['watch_paths'], true);
            $blank = watch_paths_blank($value);
            $triggered = watch_paths_triggered($value, [$file]);
            $results[] = ['blank' => $blank, 'triggered' => $triggered, 'deploy' => $blank || $triggered];
            break;
        case 'compiled':
            $set = $case['compiled'];
            foreach ($set['all_of'] ?? [] as $r => $ruleset) {
                foreach ($ruleset['rules'] ?? [] as $i => $rule) {
                    foreach (['path', 'pattern'] as $field) {
                        if (isset($rule[$field])) {
                            $set['all_of'][$r]['rules'][$i][$field] = base64_decode($rule[$field], true);
                        }
                    }
                }
            }
            compiled_set_validate($set);
            $results[] = ['match' => compiled_set_matches($set, $file)];
            break;
        default:
            throw new InvalidArgumentException('unknown mode');
    }
}

echo json_encode(['results' => $results], JSON_THROW_ON_ERROR), "\n";
