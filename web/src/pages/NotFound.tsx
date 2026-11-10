import { Link } from 'react-router-dom';

export function NotFound() {
  return (
    <div className="empty" style={{ paddingTop: 80 }}>
      <strong>Page not found</strong>
      <Link to="/">Back to tests</Link>
    </div>
  );
}
