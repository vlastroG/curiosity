import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';

const PLUGINS = [remarkGfm];

const COMPONENTS = {
  a({ href, children }) {
    return (
      <a href={href} target="_blank" rel="noopener noreferrer">
        {children}
      </a>
    );
  },
  // широкая таблица прокручивается сама, а не растягивает страницу
  table({ children }) {
    return (
      <div className="table-wrap">
        <table>{children}</table>
      </div>
    );
  },
};

// Markdown -- ответ модели; сырой HTML не отображается.
export default function Markdown({ text }) {
  return (
    <div className="markdown">
      <ReactMarkdown remarkPlugins={PLUGINS} components={COMPONENTS}>
        {text}
      </ReactMarkdown>
    </div>
  );
}
