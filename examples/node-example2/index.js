const express = require('express');
const app = express();


app.get('/user/:id', function(req, res) {
  const id = req.param('id');
  const format = req.param('format', 'json');
  res.send(`User ${id} (format: ${format})`);
});


app.del('/user/:id', function(req, res) {
  const id = req.param('id');
  res.json({ deleted: id });
});

app.listen(3000, () => {
  console.log('Server running on port 3000');
});
