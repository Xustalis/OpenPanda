import type { ReactNode } from "react";
import CopyButton from "./CopyButton";

type Language = "yaml" | "json";

const VALUE_TOKENS =
  /("[^"]*"|'[^']*'|\[[^\]]*\]|\{[^}]*\}|-?\b\d+(?:\.\d+)?\b|\b(?:true|false|null)\b)/g;

function valueTokens(value: string, prefix: string): ReactNode[] {
  return value.split(VALUE_TOKENS).map((part, index) => {
    if (!part) return null;
    let className = "tok-value";
    if (/^["']/.test(part)) className = "tok-string";
    else if (part.startsWith("[")) className = "tok-array";
    else if (/^-?\d/.test(part)) className = "tok-num";
    else if (/^(?:true|false|null)$/.test(part)) className = "tok-bool";
    return (
      <span key={`${prefix}-${index}`} className={className}>
        {part}
      </span>
    );
  });
}

/** Naive but stable YAML comment splitter: ignores `#` inside quotes. */
function splitComment(line: string): [string, string] {
  let quote: string | null = null;
  for (let index = 0; index < line.length; index += 1) {
    const character = line[index];
    if (quote) {
      if (character === quote) quote = null;
    } else if (character === '"' || character === "'") {
      quote = character;
    } else if (character === "#") {
      return [line.slice(0, index), line.slice(index)];
    }
  }
  return [line, ""];
}

function highlightYaml(line: string, lineIndex: number): ReactNode {
  const [code, comment] = splitComment(line);
  const nodes: ReactNode[] = [];
  const keyMatch = code.match(/^(\s*(?:-\s+)?)([A-Za-z0-9_./-]+)(:)(.*)$/);

  if (keyMatch) {
    nodes.push(
      <span key="lead" className="tok-punc">
        {keyMatch[1]}
      </span>,
      <span key="key" className="tok-key">
        {keyMatch[2]}
      </span>,
      <span key="colon" className="tok-punc">
        {keyMatch[3]}
      </span>,
      ...valueTokens(keyMatch[4], `${lineIndex}-v`),
    );
  } else {
    const listMatch = code.match(/^(\s*-\s+)(.*)$/);
    if (listMatch) {
      nodes.push(
        <span key="dash" className="tok-punc">
          {listMatch[1]}
        </span>,
        ...valueTokens(listMatch[2], `${lineIndex}-l`),
      );
    } else {
      nodes.push(...valueTokens(code, `${lineIndex}-p`));
    }
  }

  if (comment) {
    nodes.push(
      <span key="comment" className="tok-comment">
        {comment}
      </span>,
    );
  }

  return nodes.length > 0 ? nodes : " ";
}

function highlightJson(line: string, lineIndex: number): ReactNode {
  const token = /("(?:\\.|[^"\\])*")(\s*:)?|(-?\d+(?:\.\d+)?)|\b(true|false|null)\b/g;
  const nodes: ReactNode[] = [];
  let last = 0;
  let counter = 0;
  let match: RegExpExecArray | null;

  while ((match = token.exec(line)) !== null) {
    if (match.index > last) {
      nodes.push(
        <span key={`p-${lineIndex}-${counter++}`} className="tok-punc">
          {line.slice(last, match.index)}
        </span>,
      );
    }
    if (match[1]) {
      nodes.push(
        <span
          key={`s-${lineIndex}-${counter++}`}
          className={match[2] ? "tok-key" : "tok-string"}
        >
          {match[1]}
        </span>,
      );
      if (match[2]) {
        nodes.push(
          <span key={`c-${lineIndex}-${counter++}`} className="tok-punc">
            {match[2]}
          </span>,
        );
      }
    } else if (match[3]) {
      nodes.push(
        <span key={`n-${lineIndex}-${counter++}`} className="tok-num">
          {match[3]}
        </span>,
      );
    } else if (match[4]) {
      nodes.push(
        <span key={`b-${lineIndex}-${counter++}`} className="tok-bool">
          {match[4]}
        </span>,
      );
    }
    last = token.lastIndex;
  }

  if (last < line.length) {
    nodes.push(
      <span key={`tail-${lineIndex}`} className="tok-punc">
        {line.slice(last)}
      </span>,
    );
  }

  return nodes.length > 0 ? nodes : " ";
}

type CodeBlockProps = {
  filename: string;
  language: Language;
  code: string;
};

export default function CodeBlock({ filename, language, code }: CodeBlockProps) {
  const lines = code.replace(/\n$/, "").split("\n");

  return (
    <div className="code-window">
      <div className="code-head">
        <span className="code-dots" aria-hidden="true">
          <i />
          <i />
          <i />
        </span>
        <span className="code-filename">{filename}</span>
        <span className="code-lang">{language}</span>
        <span className="copy-wrap">
          <CopyButton value={code} />
        </span>
      </div>
      <pre className="code-body" aria-label={`${filename} 代码示例`}>
        <code>
          {lines.map((line, index) => (
            <span className="code-line" key={index}>
              <span className="code-ln">{String(index + 1).padStart(2, "0")}</span>
              <span className="code-src">
                {language === "yaml"
                  ? highlightYaml(line, index)
                  : highlightJson(line, index)}
              </span>
            </span>
          ))}
        </code>
      </pre>
    </div>
  );
}
