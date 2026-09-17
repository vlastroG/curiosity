import { parseBlocks } from './markdown.js';

/**
 * Отрисовка разобранного Markdown в React-элементы.
 *
 * Разбор живёт в markdown.js и покрыт тестами; здесь остаётся только превращение
 * описаний блоков в узлы. Библиотеку не берём по двум причинам: нужен небольшой
 * поднабор разметки, и вывод модели нельзя вставлять как HTML -- здесь текст
 * никогда не превращается в разметку, только в React-узлы.
 */

const INLINE = /(\*\*[^*\n]+\*\*|`[^`\n]+`|\*[^*\n]+\*)/g;

function inline(text) {
  return text
    .split(INLINE)
    .filter((part) => part !== '')
    .map((part, index) => {
      if (part.startsWith('**') && part.endsWith('**')) {
        return <strong key={index}>{part.slice(2, -2)}</strong>;
      }
      if (part.startsWith('`') && part.endsWith('`')) {
        return <code key={index}>{part.slice(1, -1)}</code>;
      }
      if (part.startsWith('*') && part.endsWith('*')) {
        return <em key={index}>{part.slice(1, -1)}</em>;
      }
      return part;
    });
}

function Table({ header, align, rows }) {
  return (
    <div className="md-table-wrap">
      <table className="md-table">
        <thead>
          <tr>
            {header.map((cell, index) => (
              <th key={index} style={{ textAlign: align[index] }}>
                {inline(cell)}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row, rowIndex) => (
            <tr key={rowIndex}>
              {row.map((cell, index) => (
                <td key={index} style={{ textAlign: align[index] }}>
                  {inline(cell)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function block(item, key) {
  switch (item.type) {
    case 'code':
      return (
        <pre key={key} className="md-code">
          {item.text}
        </pre>
      );

    case 'rule':
      return <hr key={key} className="md-rule" />;

    case 'table':
      return <Table key={key} header={item.header} align={item.align} rows={item.rows} />;

    case 'heading': {
      const Tag = `h${item.level}`;
      return (
        <Tag key={key} className="md-h">
          {inline(item.text)}
        </Tag>
      );
    }

    case 'list': {
      const items = item.items.map((text, index) => <li key={index}>{inline(text)}</li>);
      return item.ordered ? (
        <ol key={key} className="md-list" start={item.start}>
          {items}
        </ol>
      ) : (
        <ul key={key} className="md-list">
          {items}
        </ul>
      );
    }

    default:
      return (
        <p key={key} className="md-p">
          {inline(item.text)}
        </p>
      );
  }
}

export function Markdown({ text }) {
  if (!text) return null;

  return <div className="md">{parseBlocks(text).map(block)}</div>;
}
