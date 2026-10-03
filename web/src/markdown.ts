import MarkdownIt from "markdown-it";

const md = new MarkdownIt({ html: false, linkify: false, typographer: false });

// No executable HTML, embedded data, or custom protocols in saved documents.
md.validateLink = (url) => !/[\x00-\x20\\]/.test(url) &&
  (!/^[a-z][a-z0-9+.-]*:/i.test(url) || /^(https?:|mailto:)/i.test(url));

md.renderer.rules.link_open = (tokens, index, options, _env, renderer) => {
  const token = tokens[index];
  if (!String(token.attrGet("href") || "").startsWith("#")) token.attrSet("target", "_blank");
  token.attrSet("rel", "noopener noreferrer");
  return renderer.renderToken(tokens, index, options);
};

// Rendering a document must not silently contact external image hosts.
md.renderer.rules.image = (tokens, index) => {
  const token = tokens[index];
  const url = md.utils.escapeHtml(String(token.attrGet("src") || ""));
  const label = md.utils.escapeHtml(token.content || "查看图片");
  return `<a class="markdown-image-link" href="${url}" target="_blank" rel="noopener noreferrer">图片：${label}</a>`;
};

md.renderer.rules.heading_open = (tokens, index, options, env, renderer) => {
  const text = tokens[index + 1]?.content || "section";
  const base = text.toLowerCase().trim().replace(/[^\p{L}\p{N}\s_-]/gu, "").replace(/\s+/g, "-") || "section";
  const headings = env?.headings as Map<string, number>;
  const count = headings.get(base) || 0;
  headings.set(base, count + 1);
  tokens[index].attrSet("id", "md-" + base + (count ? "-" + count : ""));
  return renderer.renderToken(tokens, index, options);
};

export function renderMarkdown(body: string): string {
  return md.render(body, { headings: new Map<string, number>() });
}
