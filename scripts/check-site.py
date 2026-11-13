#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
"""check-site.py — static guard for the marketing site in site/.

The site has no build step: GitHub Pages publishes site/ exactly as committed,
and `make gate` never looks at it. Every invariant below is a regression that
was either already paid for elsewhere in this repo or is cheap to introduce and
expensive to notice in production.

  1. i18n key coverage, both directions, across all nine pages. A missing
     English key silently renders Chinese on the English page; an unused key is
     dead copy that will drift on the next edit. 404.html keeps its own inline
     table because it must work with no other asset available.
  2. Structure: unique ids, in-page and cross-page anchors, every local asset
     (HTML, CSS url(), importmap, scene.js textures), every internal page link,
     and one identical site-level nav on every page.
  3. SEO declarations: one canonical per page, matching og:url, no hreflang,
     structured data on the landing page only.
  4. Content stays in step with the repository: version literals agree with the
     newest CHANGELOG heading, changelog.html lists every 0.1.x release,
     download.html lists every release target, and the capability / security /
     FAQ / community pages keep their item counts above the floor that makes
     them worth a standalone URL.
  5. Sitemap lists exactly the pages that exist, and its lastmod is not older
     than the newest release. All self-referential URLs share one host.
  6. External links opened in a new tab carry rel=noopener.

Stdlib only, no network, no git. Exits non-zero on any failure.
"""

from __future__ import annotations

import datetime
import html as htmlmod
import json
import re
import sys
import xml.etree.ElementTree as ET
from pathlib import Path
from urllib.parse import urlparse

ROOT = Path(__file__).resolve().parents[1]
SITE = ROOT / 'site'

PAGES = ('index.html', 'features.html', 'docs.html', 'download.html',
         'changelog.html', 'status.html', 'security.html', 'community.html', 'faq.html')
pages = {name: (SITE / name).read_text(encoding='utf-8') for name in PAGES}
SUBPAGES = tuple(n for n in PAGES if n != 'index.html')
index = pages['index.html']
notfound = (SITE / '404.html').read_text(encoding='utf-8')
i18n = (SITE / 'js' / 'i18n.js').read_text(encoding='utf-8')
scene = (SITE / 'js' / 'scene.js').read_text(encoding='utf-8')
css = (SITE / 'css' / 'site.css').read_text(encoding='utf-8')
changelog = (ROOT / 'CHANGELOG.md').read_text(encoding='utf-8')
sitemap = (SITE / 'sitemap.xml').read_text(encoding='utf-8')
robots = (SITE / 'robots.txt').read_text(encoding='utf-8')

failures: list[str] = []
count = 0


def check(name: str, ok: bool, detail: str = '') -> None:
    global count
    count += 1
    if ok:
        print(f'  ok   {name}')
    else:
        print(f'  FAIL {name}')
        if detail:
            for line in detail.splitlines():
                print(f'       {line}')
        failures.append(name)


def norm(text: str) -> str:
    return re.sub(r'\s+', '', htmlmod.unescape(re.sub(r'<[^>]+>', '', text)))


print('site/ static checks')

# ── 1. i18n keys ────────────────────────────────────────────────────────────
ATTR = r'data-i18n(?:-aria|-placeholder|-content|-title)?="([^"]+)"'
markup_keys: set[str] = set()
for text in pages.values():
    markup_keys |= set(re.findall(ATTR, text))
en_body = i18n.split('export const EN = {', 1)[1].split('\n}', 1)[0]
en_keys = set(re.findall(r"^\s*['\"]([A-Za-z0-9_.\-]+)['\"]\s*:", en_body, re.M))
en_keys |= set(re.findall(r'^\s*([A-Za-z0-9_.\-]+)\s*:', en_body, re.M))
missing = sorted(markup_keys - en_keys)
unused = sorted(en_keys - markup_keys)
check('i18n: every data-i18n key on every page has an English entry',
      not missing, 'missing: ' + ', '.join(missing))
check('i18n: no orphaned English keys',
      not unused, 'unused: ' + ', '.join(unused))

nf_keys = set(re.findall(r'data-i18n="([^"]+)"', notfound))
nf_block = re.search(r'var EN = \{(.*?)\n  \}', notfound, re.S)
nf_en = set(re.findall(r'^\s*([A-Za-z][A-Za-z0-9_]*)\s*:', nf_block.group(1), re.M)) if nf_block else set()
check('404.html: inline English table covers its data-i18n keys',
      bool(nf_block) and nf_keys == nf_en,
      f'markup={sorted(nf_keys)} english={sorted(nf_en)}')

# ── 2. structure ────────────────────────────────────────────────────────────
ids_by_page = {name: set(re.findall(r'\sid="([^"]+)"', text)) for name, text in pages.items()}
dupes = []
for name, text in pages.items():
    ids = re.findall(r'\sid="([^"]+)"', text)
    dupes += [f'{name}: {i}' for i in sorted({x for x in ids if ids.count(x) > 1})]
check('no duplicate ids on any page', not dupes, '\n'.join(dupes[:6]))

broken: list[str] = []
missing_pages: list[str] = []
for name, text in pages.items():
    for href in re.findall(r'href="([^"]+)"', text):
        # An in-page anchor belongs to its own document; "index.html#faq"
        # belongs to index.html. Both resolve or the check fails.
        if href.startswith('#'):
            if href[1:] not in ids_by_page[name]:
                broken.append(f'{name}: {href}')
        elif re.match(r'^[A-Za-z0-9.\-]+\.html#', href):
            target, anchor = href.split('#', 1)
            if target not in ids_by_page:
                missing_pages.append(f'{name}: {href} (no such page)')
            elif anchor not in ids_by_page[target]:
                broken.append(f'{name}: {href}')
        elif re.fullmatch(r'[A-Za-z0-9.\-]+\.html', href):
            if href not in pages:
                missing_pages.append(f'{name}: {href}')
check('every in-page and cross-page anchor resolves', not broken,
      'broken: ' + ', '.join(sorted(set(broken))))
check('every internal page link points at a real page', not missing_pages,
      'missing: ' + ', '.join(sorted(set(missing_pages))))

tabs = re.findall(r'<[^>]*\brole="tab"[^>]*>', index)
bad_tabs = [t for t in tabs if 'aria-controls=' not in t]
check('every role=tab declares aria-controls', not bad_tabs, '\n'.join(bad_tabs[:4]))
controlled = re.findall(r'aria-controls="([^"]+)"', index)
check('every tabpanel target exists',
      all(c in ids_by_page['index.html'] for c in controlled),
      'missing: ' + ', '.join(sorted(c for c in controlled if c not in ids_by_page['index.html'])))

refs: list[tuple[Path, str]] = []
for text in (*pages.values(), notfound):
    for ref in re.findall(r'(?:href|src)="([^"]+)"', text):
        # A leading / is a site-root URL, not a file relative to site/; those
        # are banned outright by the /OpenPanda/ check below.
        if not re.match(r'^(https?:|mailto:|#|data:|/)', ref):
            refs.append((SITE, ref.split('?')[0].split('#')[0]))
for url in re.findall(r'url\(([^)]+)\)', css):
    url = url.strip().strip('"\'').split('?')[0]
    if not url.startswith(('http', 'data:')):
        refs.append((SITE / 'css', url))
for url in re.findall(r"['\"](textures/[^'\"]+)['\"]", scene):
    refs.append((SITE, url))
for url in re.findall(r'["\'](\./vendor/[^"\']+)["\']', index):
    refs.append((SITE, url))
missing_assets = sorted({ref for base, ref in refs if not (base / ref).resolve().exists()})
check('every local asset reference exists', not missing_assets,
      'missing: ' + ', '.join(missing_assets))
check('no root-relative /OpenPanda/ paths',
      not any(re.search(r'["\'](/OpenPanda/)', t) for t in (*pages.values(), notfound, css)),
      'use the canonical absolute URL instead; see the head note in index.html')

# CLI examples use <id>, <url>, <version> and friends. Unescaped, the browser
# parses them as unknown elements and swallows the rest of the sentence — and
# English copy is injected with innerHTML, so the dictionary needs the same
# escaping as the markup. This was a real regression caught by a DOM parse.
PLACEHOLDERS = re.compile(
    r'<(?:id|url|version|os|arch|addr|dir|data|file|name|path|key|model|'
    r'host|port|task|n|peer address)>')
raw_placeholders = []
for fname, text in (*pages.items(), ('404.html', notfound), ('i18n.js', i18n)):
    for m in PLACEHOLDERS.finditer(text):
        raw_placeholders.append(f'{fname}: {m.group(0)}')
check('CLI placeholders are HTML-escaped in both markup and dictionary',
      not raw_placeholders, 'raw: ' + ', '.join(sorted(set(raw_placeholders))))

# The nav is the thing that makes this a site rather than a single long page.
NAV = r'<div class="nav-links">(.*?)</div>'
nav_sets: dict[str, set[str]] = {}
current_marks: dict[str, list[str]] = {}
for name, text in pages.items():
    block = re.search(NAV, text, re.S)
    links = re.findall(r'<a\b[^>]*>', block.group(1)) if block else []
    nav_sets[name] = set(re.findall(r'href="([^"]+)"', ' '.join(links)))
    current_marks[name] = [
        m.group(1) for a in links if 'aria-current="page"' in a
        for m in [re.search(r'href="([^"]+)"', a)] if m
    ]
site_links = nav_sets['index.html']
check(f'all {len(PAGES)} pages share one site-level nav',
      all(nav_sets[n] == site_links for n in PAGES),
      '\n'.join(f'{n}: differs by {sorted(nav_sets[n] ^ site_links)}'
                for n in PAGES if nav_sets[n] != site_links))
# A site does not need every page in the top bar, but it does need every page
# reachable from the landing page (nav + site map + footer all count).
landing_targets = {h.split('#')[0] for h in re.findall(r'href="([^"]+\.html)', index)}
check('every page is reachable from the landing page',
      set(SUBPAGES) <= landing_targets,
      'unreachable: ' + ', '.join(sorted(set(SUBPAGES) - landing_targets)))
check('the nav covers the primary pages',
      len({h.split('#')[0] for h in site_links} & set(PAGES)) >= 7,
      'nav links: ' + ', '.join(sorted(site_links)))
nav_targets = {h.split('#')[0] for h in site_links}
# A page absent from the top bar (security, community) cannot mark itself
# there; it is reached from the site map and the footer instead.
check('each page in the nav marks exactly itself with aria-current',
      all((current_marks.get(n) == [n]) if n in nav_targets
          else not current_marks.get(n) for n in PAGES),
      '\n'.join(f'{n}: {current_marks.get(n)}' for n in PAGES
                if current_marks.get(n) and current_marks.get(n) != [n]))

# No build step and no server-side include: the footer and the language
# bootstrap are literally copied into each document. Detect a one-page edit.
footers = {n: re.search(r'<footer class="site-footer">.*?</footer>', t, re.S)
           for n, t in pages.items()}
base_footer = footers['index.html'].group(0) if footers['index.html'] else ''
check(f'all {len(PAGES)} pages share one footer, byte for byte',
      bool(base_footer) and all(m and m.group(0) == base_footer for m in footers.values()),
      'a page footer differs from index.html')
boots = {n: re.search(r'<!-- Language bootstrap.*?</noscript>', t, re.S)
         for n, t in pages.items()}
base_boot = boots['index.html'].group(0) if boots['index.html'] else ''
check(f'all {len(PAGES)} pages share one language bootstrap, byte for byte',
      bool(base_boot) and all(m and m.group(0) == base_boot for m in boots.values()),
      'a page bootstrap differs from index.html')
check('landing page loads main.js; content pages load page.js',
      'src="js/main.js"' in index
      and all('src="js/page.js"' in pages[n] and 'src="js/main.js"' not in pages[n]
              for n in PAGES if n != 'index.html'))

# ── 3. structured data & SEO declarations ──────────────────────────────────
ld_block = re.search(r'<script type="application/ld\+json">(.*?)</script>', index, re.S)
ld = json.loads(ld_block.group(1)) if ld_block else None
graph = ld.get('@graph', []) if isinstance(ld, dict) else []
app = next((x for x in graph if x.get('@type') == 'SoftwareApplication'), None)
check('JSON-LD parses and has one SoftwareApplication', bool(app))
ld_pages = [n for n, t in pages.items() if 'application/ld+json' in t]
check('structured data is declared on the landing page only',
      ld_pages == ['index.html'], 'found on: ' + ', '.join(ld_pages))

faq_nodes = [x for x in graph if x.get('@type') == 'FAQPage']
if faq_nodes:
    # If an FAQPage marker is ever restored it must stay byte-for-byte in step
    # with the visible answers (pure text, links stripped) or it is cloaking.
    visible = re.findall(
        r'<details class="faq[^"]*"[^>]*>\s*<summary>\s*<span class="faq-q"[^>]*>(.*?)</span>'
        r'.*?<div class="faq-body"><p[^>]*>(.*?)</p>', index, re.S)
    entities = faq_nodes[0].get('mainEntity', [])
    diffs = []
    if len(visible) != len(entities):
        diffs.append(f'{len(visible)} visible answers vs {len(entities)} marked answers')
    for i, (q, a) in enumerate(visible):
        if i >= len(entities):
            break
        qa, aa = entities[i].get('name', ''), entities[i].get('acceptedAnswer', {}).get('text', '')
        if norm(q) != norm(qa):
            diffs.append(f'Q{i + 1} differs from the visible question')
        if norm(a) != norm(aa):
            diffs.append(f'A{i + 1} differs from the visible answer')
    check('FAQPage markup matches the visible FAQ', not diffs, '\n'.join(diffs))

check('no hreflang / og:locale:alternate declarations on any page',
      all('hreflang=' not in t and 'og:locale:alternate' not in t for t in pages.values()),
      'Chinese is the only canonical edition; see the head note in index.html')
check('og:locale stays zh_CN on every page',
      all('<meta property="og:locale" content="zh_CN" />' in t for t in pages.values()))
check('every page has a title and a description',
      all('data-i18n-content="meta.' in t and '<meta name="description"' in t
          for t in pages.values()))

# ── 4. versions & repository facts ─────────────────────────────────────────
release = re.search(r'^##\s+\[(\d+\.\d+\.\d+)\]\s*-\s*(\d{4}-\d{2}-\d{2})', changelog, re.M)
check('CHANGELOG has a parseable newest release', bool(release))
latest, release_day = (release.group(1), release.group(2)) if release else ('', '')
if app:
    check('JSON-LD softwareVersion matches newest release',
          app.get('softwareVersion') == latest,
          f'softwareVersion={app.get("softwareVersion")} changelog={latest}')

CURRENT = re.compile(
    r'当前版本|最新版本|默认取最新|默认装最新|一键安装默认|softwareVersion|'
    r'current version|latest stable|Latest\b|Current\b|stable')
stale = []
sources = {**pages, 'i18n.js': i18n}
for fname, text in sources.items():
    for no, line in enumerate(text.splitlines(), 1):
        if not CURRENT.search(line):
            continue
        versions = set(re.findall(r'v(\d+\.\d+\.\d+)', line))
        if versions and latest not in versions:
            stale.append(f'{fname}:{no}: {line.strip()[:110]}')
check('current-version lines name the newest release', not stale, '\n'.join(stale[:6]))

all_versions = set(re.findall(r'v(\d+\.\d+\.\d+)', '\n'.join(pages.values()) + i18n))


def semver(v: str) -> tuple[int, ...]:
    return tuple(int(p) for p in v.split('.'))


future = sorted(v for v in all_versions if semver(v) > semver(latest)) if latest else []
check('no version literal newer than the newest release', not future,
      'unexpected: ' + ', '.join(future))

md_releases = {v for v in re.findall(r'^## \[(\d+\.\d+\.\d+)\]', changelog, re.M)
               if v.startswith('0.1.')}
site_releases = set(re.findall(r'class="pg-release-ver">v(\d+\.\d+\.\d+)<',
                               pages['changelog.html']))
check('changelog.html lists every 0.1.x release in CHANGELOG.md',
      bool(md_releases) and site_releases == md_releases,
      f'site={sorted(site_releases)} changelog={sorted(md_releases)}')

rows = re.findall(r'<tr><td>([^<]+)</td><td>([^<]+)</td><td>', pages['download.html'])
targets = {(os.strip(), arch.strip()) for os, arch in rows}
expected_targets = {
    ('macOS', 'arm64'), ('macOS', 'amd64'),
    ('Linux', 'amd64'), ('Linux', 'arm64'),
    ('Windows', 'amd64'), ('Windows', 'arm64'),
    ('lite-linux', 'amd64'), ('lite-linux', 'arm64'), ('lite-linux', 'armv7'),
}
check('download.html covers all six release targets plus all three lite builds',
      targets == expected_targets,
      f'missing={sorted(expected_targets - targets)} extra={sorted(targets - expected_targets)}')

# ── 4b. content density & cross-page facts ─────────────────────────────────
# A standalone URL earns its place by holding real information. These floors
# stop a future edit from quietly reducing a page back to a stub.
feature_facts = len(re.findall(r'<div class="pg-fact">', pages['features.html']))
check('features.html carries at least 25 capability entries', feature_facts >= 25,
      f'found {feature_facts}')
redlines = len(re.findall(r'data-i18n="sc\.s4\.b\d+"', pages['security.html']))
check('security.html states all five deployment red lines', redlines == 5,
      f'found {redlines}')
faq_items = len(re.findall(r'<details class="pg-faq">', pages['faq.html']))
check('faq.html carries all 29 questions', faq_items == 29, f'found {faq_items}')
adapter_rows = re.findall(r'<code>([a-z_]+\.py)</code>', pages['docs.html'])
# A prepared release must not strand first-time users on a stale tag URL.
check('download entry follows the published stable channel',
      'href="https://github.com/Xustalis/OpenPanda/releases/latest"' in pages['download.html'])
check('docs include an addressable first-task acceptance and recovery guide',
      'id="first-task"' in pages['docs.html'] and 'href="#first-task"' in pages['docs.html']
      and all(f'data-i18n="docs.first.{part}"' in pages['docs.html']
              for part in ['model', 'agent', 'run', 'watch', 'accept', 'recover', 'upgrade']))
check('docs.html lists the eleven adapter scripts', len(adapter_rows) == 11,
      f'found {adapter_rows}')
check('community.html documents the local gates',
      all(x in pages['community.html'] for x in
          ('make gate', 'make gate-all', 'npm run typecheck')))
hub_cards = len(re.findall(r'<a class="hub-card"', index))
check('the landing site map links every subpage',
      hub_cards == len(SUBPAGES), f'{hub_cards} cards vs {len(SUBPAGES)} subpages')
thin = [n for n in SUBPAGES
        if len(re.findall(r'<section class="pg-section[ "]', pages[n])) < 4]
check('every subpage has at least four substantive sections', not thin,
      'thin: ' + ', '.join(thin))
toc_broken = []
for name, text in pages.items():
    aside = re.search(r'<aside class="pg-toc">.*?</aside>', text, re.S)
    if not aside:
        continue
    for anchor in re.findall(r'href="#([^"]+)"', aside.group(0)):
        if anchor not in ids_by_page[name]:
            toc_broken.append(f'{name}: #{anchor}')
check('every table-of-contents link resolves on its own page', not toc_broken,
      'broken: ' + ', '.join(toc_broken))

# ── 5. canonical host & sitemap ────────────────────────────────────────────
canon = re.search(r'<link rel="canonical" href="([^"]+)"', index)
BASE = canon.group(1) if canon else ''
check('canonical is present and absolute', bool(BASE) and BASE.startswith('https://'))
parsed = urlparse(BASE)
root_prefix, host = parsed.path, parsed.netloc

canon_by_page = {n: re.findall(r'<link rel="canonical" href="([^"]+)"', t)
                 for n, t in pages.items()}
expected_canon = {n: BASE if n == 'index.html' else BASE + n for n in PAGES}
check('each page has exactly one canonical, pointing at itself',
      all(canon_by_page[n] == [expected_canon[n]] for n in PAGES),
      '\n'.join(f'{n}: {canon_by_page[n]} expected {[expected_canon[n]]}'
                for n in PAGES if canon_by_page[n] != [expected_canon[n]]))
og_urls = {n: re.findall(r'<meta property="og:url" content="([^"]+)"', t)
           for n, t in pages.items()}
check('every og:url equals its own canonical',
      all(og_urls[n] == [expected_canon[n]] for n in PAGES),
      '\n'.join(f'{n}: {og_urls[n]}' for n in PAGES
                if og_urls[n] != [expected_canon[n]]))

hosted: list[str] = []
hosted += re.findall(r'<meta property="og:image" content="([^"]+)"', index)
hosted += re.findall(r'<meta name="twitter:image" content="([^"]+)"', index)
if app:
    hosted.append(app.get('url', ''))
nf_urls = re.findall(r'href="([^"]+)"', notfound) + re.findall(r"url\('([^']+)'\)", notfound)
# Only URLs that point back at this site — the GitHub links in 404.html are
# external by design and must not be forced onto the canonical host.
hosted += [u for u in nf_urls if u.startswith(root_prefix) or host in u]
offhost = sorted({u for u in hosted if u and not u.startswith(BASE)})
check('every self-referential URL uses the canonical host',
      bool(BASE) and not offhost, 'off-host: ' + ', '.join(offhost))

sm = ET.fromstring(sitemap)
ns = '{http://www.sitemaps.org/schemas/sitemap/0.9}'
entries = sm.findall(f'{ns}url') or sm.findall('url')
sm_locs = []
for entry in entries:
    loc = entry.find(f'{ns}loc')
    sm_locs.append((loc.text if loc is not None else '') or '')
lastmods = []
for entry in entries:
    lm = entry.find(f'{ns}lastmod')
    lastmods.append((lm.text if lm is not None else '') or '')
check(f'sitemap lists exactly the {len(PAGES)} pages, once each',
      len(sm_locs) == len(PAGES) and set(sm_locs) == set(expected_canon.values()),
      f'sitemap={sorted(sm_locs)} pages={sorted(expected_canon.values())}')
check('every sitemap entry has a lastmod',
      len(lastmods) == len(entries) and all(lastmods))
try:
    lm_days = [datetime.date.fromisoformat(d) for d in lastmods]
    rd = datetime.date.fromisoformat(release_day)
    ok, detail = bool(lm_days) and min(lm_days) >= rd, f'oldest lastmod={min(lm_days)} release={rd}'
except ValueError:
    ok, detail = False, f'unparseable lastmod={lastmods!r}'
check('sitemap lastmod is valid and not older than the newest release', ok, detail)
check('robots.txt points at the canonical sitemap',
      re.findall(r'^Sitemap:\s*(\S+)', robots, re.M) == [BASE + 'sitemap.xml'])

# ── 6. external links ───────────────────────────────────────────────────────
unsafe = []
for fname, text in (*pages.items(), ('404.html', notfound), ('i18n.js', i18n)):
    for tag in re.findall(r'<a\b[^>]*>', text):
        if 'target="_blank"' in tag and 'noopener' not in tag:
            unsafe.append(f'{fname}: {tag[:130]}')
check('every target=_blank link carries rel=noopener', not unsafe, '\n'.join(unsafe[:6]))

print(f'\n{count - len(failures)}/{count} checks passed')
if failures:
    print('FAILED: ' + ', '.join(failures))
    sys.exit(1)
print('site/ OK')
