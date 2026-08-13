let mermaidPromise: Promise<(typeof import("mermaid"))["default"]> | undefined;
let purifierPromise: Promise<(typeof import("dompurify"))["default"]> | undefined;
let initialized = false;
let renderSequence = 0;

async function loadMermaid() {
  mermaidPromise ??= import("mermaid").then((module) => module.default);
  const mermaid = await mermaidPromise;
  if (!initialized) {
    // htmlLabels is a global option; the per-diagram flowchart.htmlLabels form is
    // deprecated and does not switch label rendering. Turning it off keeps every
    // label an SVG <text> node, so no foreignObject/XHTML ever reaches the DOM —
    // that is what makes the sanitizing step below simple and the labels visible.
    mermaid.initialize({
      startOnLoad: false,
      securityLevel: "strict",
      suppressErrorRendering: true,
      htmlLabels: false,
      theme: "base",
      themeVariables: {
        darkMode: false,
        background: "#ffffff",
        primaryColor: "#f4f4f5",
        primaryTextColor: "#111827",
        primaryBorderColor: "#6b7280",
        secondaryColor: "#fefce8",
        tertiaryColor: "#f8fafc",
        lineColor: "#374151",
        clusterBkg: "#fffde7",
        clusterBorder: "#c9bc64",
        fontFamily: "-apple-system, BlinkMacSystemFont, Segoe UI, PingFang SC, Microsoft YaHei, sans-serif",
      },
      flowchart: { htmlLabels: false },
    });
    initialized = true;
  }
  return mermaid;
}

function containsExternalURL(value: string): boolean {
  for (const match of value.matchAll(/url\(\s*(['"]?)(.*?)\1\s*\)/gi)) {
    if (!match[2].trim().startsWith("#")) return true;
  }
  return false;
}

/**
 * Mermaid output is not guaranteed to be well-formed XML, so an SVG-namespace
 * parse is only the preferred path: an HTML parse is tolerant and still yields
 * a real <svg> element to inspect.
 */
function parseSVG(markup: string): SVGElement {
  const xml = new DOMParser().parseFromString(markup, "image/svg+xml");
  if (!xml.querySelector("parsererror") && xml.documentElement.localName.toLowerCase() === "svg") {
    return xml.documentElement as unknown as SVGElement;
  }
  const html = new DOMParser().parseFromString(markup, "text/html");
  const svg = html.querySelector("svg");
  if (!svg) throw new Error("Mermaid returned no SVG element");
  return svg;
}

async function sanitizeSVG(input: string): Promise<string> {
  purifierPromise ??= import("dompurify").then((module) => module.default);
  const purifier = await purifierPromise;
  // Labels are plain SVG text now, so foreignObject can stay forbidden: it is the
  // element that carries embeddable HTML (and the img/src escape used by known
  // Mermaid XSS reports).
  const clean = purifier.sanitize(input, {
    USE_PROFILES: { svg: true, svgFilters: true },
    FORBID_TAGS: ["script", "foreignObject", "a", "use", "image"],
  });

  const svg = parseSVG(clean);
  for (const element of [svg, ...Array.from(svg.querySelectorAll("*"))]) {
    for (const attribute of Array.from(element.attributes)) {
      const name = attribute.name.toLowerCase();
      const value = attribute.value.trim();
      if (name.startsWith("on") || name === "src") {
        element.removeAttribute(attribute.name);
      } else if ((name === "href" || name === "xlink:href") && !value.startsWith("#")) {
        element.removeAttribute(attribute.name);
      } else if (containsExternalURL(value)) {
        element.removeAttribute(attribute.name);
      }
    }
  }

  for (const style of Array.from(svg.querySelectorAll("style"))) {
    const css = style.textContent ?? "";
    if (/@import|expression\s*\(|javascript:|data:/i.test(css) || containsExternalURL(css)) {
      style.remove();
    }
  }
  return svg.outerHTML;
}

export async function renderMermaid(source: string): Promise<string> {
  const mermaid = await loadMermaid();
  const id = `pi-mermaid-${++renderSequence}`;
  const result = await mermaid.render(id, source);
  return sanitizeSVG(result.svg);
}
