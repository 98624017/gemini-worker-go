ALTER TABLE tasks
ADD COLUMN request_protocol TEXT NOT NULL DEFAULT 'gemini_generate_content'
CHECK (
  request_protocol IN (
    'gemini_generate_content',
    'openai_image_generation'
  )
);
