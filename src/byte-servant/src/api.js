export async function sendMessage(messages) {
  const apiKey = window._env_?.DEEPSEEK_API_KEY;
  if (!apiKey) {
    throw new Error(
      'DEEPSEEK_API_KEY is not set. Start the container with -e DEEPSEEK_API_KEY=<your key>.'
    );
  }

  const response = await fetch('https://api.deepseek.com/chat/completions', {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${apiKey}`,
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      model: 'deepseek-chat',
      messages,
      stream: false,
    }),
  });

  if (!response.ok) {
    const errorBody = await response.text();
    throw new Error(`DeepSeek API error ${response.status}: ${errorBody}`);
  }

  const data = await response.json();
  return data.choices[0].message.content;
}
