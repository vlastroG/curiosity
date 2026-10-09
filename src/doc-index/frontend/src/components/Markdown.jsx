import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { linkCitations } from '../format.js';

const PLUGINS = [remarkGfm];

// Markdown -- ответ модели с разметкой (заголовки, списки, код, таблицы).
// Сырой HTML не отображается. onCite -- сноски [n] становятся кнопками.
export default function Markdown({ text, onCite, lit }) {
  const components = {
    a({ href, children }) {
      const cite = onCite && /^#cite-(\d+)$/.exec(href || '');
      if (cite) {
        const n = Number(cite[1]);
        return (
          <button type="button" className={`cite ${lit === n ? 'lit' : ''}`} onClick={() => onCite(n)}>
            {n}
          </button>
        );
      }
      return (
        <a href={href} target="_blank" rel="noopener noreferrer">
          {children}
        </a>
      );
    },
    // таблица шире ответа прокручивается сама, а не растягивает страницу
    table({ children }) {
      return (
        <div className="table-wrap">
          <table>{children}</table>
        </div>
      );
    },
  };
  return (
    <div className="answer-text markdown">
      <ReactMarkdown remarkPlugins={PLUGINS} components={components}>
        {onCite ? linkCitations(text) : text}
      </ReactMarkdown>
    </div>
  );
}
