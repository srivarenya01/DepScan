const express = require('express');
const _ = require('lodash');

const app = express();

app.get('/', (req, res) => {
    const list = [1, 2, 3];
    const first = _.head(list);
    res.send(`Hello World! First element is ${first}`);
});

app.listen(3000, () => {
    console.log('App listening on port 3000');
});
