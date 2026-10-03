import test from "node:test";
import assert from "node:assert/strict";
import { renderMarkdown } from "../src/markdown.ts";

test("Markdown renders headings, lists, quotes, fenced code and tables", () => {
  const html = renderMarkdown("# 我的笔记\n\n**重要** 与 *强调*\n\n- 列表\n\n> 引用\n\n```js\nconst x = '<script>';\n```\n\n| 名称 | 标签 |\n| --- | --- |\n| 文档 | 分类 |\n");
  for (const fragment of ['id="md-我的笔记"', "<strong>重要</strong>", "<em>强调</em>", "<ul>", "<blockquote>", "language-js", "&lt;script&gt;", "<table>", "<td>分类</td>"]) assert.ok(html.includes(fragment), fragment);
});
test("raw HTML and active URL schemes cannot execute", () => {
  for (const content of ['<script>alert(1)</script>', '<svg onload="alert(1)"></svg>', '<iframe src="https://example.com"></iframe>', '[x](javascript:alert%281%29)', '[x](JaVaScRiPt:alert%281%29)', '[x](data:text/html;base64,PHNjcmlwdD4=)', '[x](vbscript:msgbox%281%29)', '[x](file:///etc/passwd)', '[x](javascript&#58;alert%281%29)', '![x](data:image/png;base64,YQ==)']) {
    const html = renderMarkdown(content);
    assert.doesNotMatch(html, /<(script|svg|iframe|img)\b/i);
    assert.doesNotMatch(html, /href="(javascript|data|vbscript|file):/i);
  }
});
test("images are opt-in links, with escaped attributes and no image requests", () => {
  const html = renderMarkdown('![合成图片](https://example.com/image.webp "title")\n\n[站点](https://example.com/?x=1&y=2)');
  assert.doesNotMatch(html, /<img\b/);
  assert.match(html, /class="markdown-image-link"/);
  assert.match(html, /图片：合成图片/);
  assert.match(html, /rel="noopener noreferrer"/);
  assert.match(html, /x=1&amp;y=2/);
});
test("heading identifiers are escaped, scoped and stable across previews", () => {
  const source = '# 重复\n\n## 重复\n\n# <script>\n';
  const first = renderMarkdown(source);
  assert.equal(first, renderMarkdown(source));
  assert.match(first, /id="md-重复"/);
  assert.match(first, /id="md-重复-1"/);
  assert.doesNotMatch(first, /id="(?:__proto__|constructor)"/);
  assert.equal(renderMarkdown(""), "");
});
