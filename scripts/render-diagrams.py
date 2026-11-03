#!/usr/bin/env python3
"""Render the animated documentation diagrams.

Each docs/assets/diagrams/src/<name>.json is a diagram document. For every
source the script renders and checks the diagram with the renderer named by
DIAGRAM_RENDERER, plays one motion cycle in the light and dark themes in
Chrome, and writes docs/assets/diagrams/<name>-light.webp and <name>-dark.webp.

Requirements: Node.js 18 or later, the renderer's command-line script in
DIAGRAM_RENDERER, Google Chrome, and Python 3 with the playwright and Pillow
packages. Run it from the repository root:
python3 scripts/render-diagrams.py [name ...]
"""

import json
import os
import pathlib
import subprocess
import sys
import tempfile

from PIL import Image
from playwright.sync_api import sync_playwright

ROOT = pathlib.Path(__file__).resolve().parent.parent
SOURCES = ROOT / "docs" / "assets" / "diagrams" / "src"
OUTPUT = ROOT / "docs" / "assets" / "diagrams"
FRAMES = 60
SCALE = 1.5
# Node-link graphs draw their nodes as circles; component diagrams keep boxes.
CIRCLE_NODES = {"seed-graph"}


def render_html(source: pathlib.Path, workdir: pathlib.Path) -> pathlib.Path:
    renderer = os.environ.get("DIAGRAM_RENDERER")
    if not renderer:
        sys.exit("set DIAGRAM_RENDERER to the diagram renderer's command-line script")
    doc = json.loads(source.read_text())
    html = workdir / f"{source.stem}.html"
    doc["meta"]["output"] = os.path.relpath(html, ROOT)
    candidate = workdir / source.name
    candidate.write_text(json.dumps(doc))
    subprocess.run(
        ["node", renderer, "finalize", doc["diagram_type"],
         str(candidate), str(html), "--repo-root", str(ROOT), "--quality", doc["meta"].get("quality_profile", "showcase"),
         "--out-dir", str(workdir / "checks")],
        cwd=ROOT, check=True, stdout=subprocess.DEVNULL,
    )
    return html


def record(browser, html: pathlib.Path, theme: str, circles: bool):
    context = browser.new_context(viewport={"width": 1600, "height": 1300}, device_scale_factor=SCALE, color_scheme=theme)
    page = context.new_page()
    page.goto(html.as_uri())
    page.wait_for_timeout(500)
    if page.evaluate("document.documentElement.getAttribute('data-theme')") != theme:
        page.click("#btn-theme")
    if page.evaluate("document.getElementById('motion-label').textContent.trim()") != "Live":
        page.click("#btn-motion")
    if circles:
        page.evaluate("""() => document.querySelectorAll('svg[data-animation] g[data-node-kind] > rect').forEach(r => {
            const half = Math.min(+r.getAttribute('width'), +r.getAttribute('height')) / 2;
            r.setAttribute('rx', half); r.setAttribute('ry', half);
        })""")
    # Let the entrance animation finish before recording the repeating cycle.
    page.wait_for_timeout(4500)
    cycle = page.evaluate("parseFloat(getComputedStyle(document.querySelector('svg[data-animation]')).getPropertyValue('--flow-cycle')) * 1000")
    page.evaluate("document.getAnimations().forEach(a => a.pause())")
    diagram = page.query_selector("svg[data-animation]")
    frames = []
    with tempfile.TemporaryDirectory() as shots:
        for k in range(FRAMES):
            # Step every animation to the same point of a later cycle so the loop is seamless.
            page.evaluate("t => document.getAnimations().forEach(a => { a.currentTime = t; })", cycle * 4 + k * cycle / FRAMES)
            shot = pathlib.Path(shots) / f"{k:03d}.png"
            diagram.screenshot(path=str(shot))
            frames.append(Image.open(shot).convert("RGB"))
    context.close()
    return frames, cycle


def main() -> None:
    names = sys.argv[1:] or sorted(p.stem for p in SOURCES.glob("*.json"))
    # The renderer writes its output relative to the repository, so the scratch directory lives there briefly.
    with tempfile.TemporaryDirectory(prefix=".diagrams-", dir=ROOT) as tmp, sync_playwright() as pw:
        browser = pw.chromium.launch(channel="chrome")
        for name in names:
            html = render_html(SOURCES / f"{name}.json", pathlib.Path(tmp))
            for theme in ("light", "dark"):
                frames, cycle = record(browser, html, theme, name in CIRCLE_NODES)
                target = OUTPUT / f"{name}-{theme}.webp"
                frames[0].save(target, save_all=True, append_images=frames[1:], duration=round(cycle / FRAMES), loop=0, quality=80, method=6)
                print(f"{target.relative_to(ROOT)}: {len(frames)} frames, {target.stat().st_size // 1024} KiB")
        browser.close()


if __name__ == "__main__":
    main()
