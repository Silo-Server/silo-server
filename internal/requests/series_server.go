package requests

import "context"

// seriesServerConnected reports whether an enabled download server
// connection takes series requests. Silo records no live health for these
// connections, so enabled is the best it can say.
func (s *Service) seriesServerConnected(ctx context.Context) (bool, error) {
	// Without the plugin service there is no router, and no download server
	// a request could reach; like moreSeasonsRequestable, read nothing.
	if s.router == nil {
		return false, nil
	}
	fc, err := s.newFulfillContext(ctx)
	if err != nil {
		return false, err
	}
	return len(seriesRouterConnections(fc)) > 0, nil
}
