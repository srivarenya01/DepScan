const jwt = require('jsonwebtoken');



const token = 'eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.e30.t-IDcSemACt8x4iTMCda8Yhe3iZaWbvV5XKSTbuAn0M';
try {
  const decoded = jwt.verify(token, 'shhhhh');
  console.log(decoded);
} catch(err) {
  console.error(err);
}
