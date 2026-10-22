// A small, safe Markdown renderer for the systems' notes (catalog data):
// headings, paragraphs, bullet and numbered lists, fenced and indented code,
// inline code, bold, italics and http(s) links. It builds DOM nodes; no
// HTML from the source is ever interpreted.

import { h } from '../util.js';

/** Renders markdown into a DocumentFragment. headingOffset shifts ## to h3 etc. */
export function renderMarkdown(src, { headingOffset = 1 } = {}) {
  const frag = document.createDocumentFragment();
  const lines = String(src ?? '').replace(/\r\n?/g, '\n').split('\n');
  let i = 0;
  let para = [];
  const flush = () => {
    if (para.length) frag.append(h('p', {}, ...inline(para.join(' '))));
    para = [];
  };
  while (i < lines.length) {
    const line = lines[i];
    let m;
    if (/^\s*```/.test(line)) {
      flush();
      const code = [];
      i++;
      while (i < lines.length && !/^\s*```/.test(lines[i])) code.push(lines[i++]);
      i++;
      frag.append(h('pre', {}, h('code', {}, code.join('\n'))));
      continue;
    }
    if ((m = line.match(/^(#{1,6})\s+(.*)$/))) {
      flush();
      const level = Math.min(6, m[1].length + headingOffset);
      frag.append(h(`h${level}`, {}, ...inline(m[2].replace(/\s*#+\s*$/, ''))));
      i++;
      continue;
    }
    if (/^\s*([-*+])\s+/.test(line) || /^\s*\d+[.)]\s+/.test(line)) {
      flush();
      const ordered = /^\s*\d+[.)]\s+/.test(line);
      const list = h(ordered ? 'ol' : 'ul');
      while (i < lines.length && (ordered ? /^\s*\d+[.)]\s+/ : /^\s*[-*+]\s+/).test(lines[i])) {
        let text = lines[i].replace(ordered ? /^\s*\d+[.)]\s+/ : /^\s*[-*+]\s+/, '');
        i++;
        // continuation lines (indented, not a new item)
        while (i < lines.length && /^\s{2,}\S/.test(lines[i]) && !/^\s*([-*+]|\d+[.)])\s+/.test(lines[i])) {
          text += ` ${lines[i].trim()}`;
          i++;
        }
        list.append(h('li', {}, ...inline(text)));
      }
      frag.append(list);
      continue;
    }
    if (/^( {4}|\t)/.test(line) && !para.length) {
      const code = [];
      while (i < lines.length && (/^( {4}|\t)/.test(lines[i]) || lines[i].trim() === '')) code.push(lines[i++].replace(/^( {4}|\t)/, ''));
      while (code.length && code[code.length - 1].trim() === '') code.pop();
      frag.append(h('pre', {}, h('code', {}, code.join('\n'))));
      continue;
    }
    if (line.trim() === '') {
      flush();
      i++;
      continue;
    }
    para.push(line.trim());
    i++;
  }
  flush();
  return frag;
}

/** Inline markup: `code`, **bold**, *italic* / _italic_, [text](http...). */
export function inline(text) {
  const out = [];
  const re = /(`+)([\s\S]*?)\1|\*\*([^*]+)\*\*|__([^_]+)__|\*([^*\s][^*]*)\*|_([^_\s][^_]*)_|\[([^\]]+)\]\(([^)\s]+)\)/g;
  let last = 0;
  let m;
  while ((m = re.exec(text))) {
    if (m.index > last) out.push(text.slice(last, m.index));
    if (m[1]) out.push(h('code', {}, m[2]));
    else if (m[3] || m[4]) out.push(h('strong', {}, ...inline(m[3] || m[4])));
    else if (m[5] || m[6]) out.push(h('em', {}, ...inline(m[5] || m[6])));
    else if (m[7]) {
      const url = m[8];
      if (/^https?:\/\//i.test(url)) out.push(h('a', { href: url, target: '_blank', rel: 'noopener noreferrer' }, m[7]));
      else out.push(m[7]);
    }
    last = re.lastIndex;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}
