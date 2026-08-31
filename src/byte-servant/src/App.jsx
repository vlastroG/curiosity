import { useEffect, useRef, useState } from 'react';
import { sendMessage } from './api';

const TYPE_INTERVAL_MS = 18;

export default function App() {
  const [messages, setMessages] = useState([]);
  const [input, setInput] = useState('');
  const [pendingReply, setPendingReply] = useState(null);
  const [shownReply, setShownReply] = useState('');
  const [isBusy, setIsBusy] = useState(false);
  const [error, setError] = useState('');
  const bottomRef = useRef(null);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [messages, shownReply]);

  useEffect(() => {
    if (pendingReply === null) return undefined;

    let index = 0;
    const interval = setInterval(() => {
      index += 1;
      setShownReply(pendingReply.slice(0, index));
      if (index >= pendingReply.length) {
        clearInterval(interval);
        setMessages((prev) => [...prev, { role: 'assistant', content: pendingReply }]);
        setPendingReply(null);
        setShownReply('');
        setIsBusy(false);
      }
    }, TYPE_INTERVAL_MS);

    return () => clearInterval(interval);
  }, [pendingReply]);

  async function handleSend() {
    const text = input.trim();
    if (!text || isBusy) return;

    setError('');
    const nextMessages = [...messages, { role: 'user', content: text }];
    setMessages(nextMessages);
    setInput('');
    setIsBusy(true);

    try {
      const reply = await sendMessage(nextMessages);
      setPendingReply(reply);
    } catch (err) {
      setError(err.message);
      setIsBusy(false);
    }
  }

  function handleKeyDown(event) {
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault();
      handleSend();
    }
  }

  return (
    <div className="app">
      <header className="app__header">
        <h1>Byte Servant</h1>
      </header>

      <main className="app__chat">
        {messages.length === 0 && pendingReply === null && (
          <p className="app__empty">Say hello to start the conversation.</p>
        )}

        {messages.map((message, i) => (
          <div key={i} className={`bubble bubble--${message.role}`}>
            {message.content}
          </div>
        ))}

        {pendingReply !== null && (
          <div className="bubble bubble--assistant">
            {shownReply}
            <span className="cursor" />
          </div>
        )}

        <div ref={bottomRef} />
      </main>

      {error && <div className="app__error">{error}</div>}

      <footer className="app__input">
        <textarea
          value={input}
          onChange={(event) => setInput(event.target.value)}
          onKeyDown={handleKeyDown}
          placeholder="Type a message..."
          disabled={isBusy}
          rows={2}
        />
        <button onClick={handleSend} disabled={isBusy || !input.trim()}>
          {isBusy ? 'Thinking...' : 'Send'}
        </button>
      </footer>
    </div>
  );
}
