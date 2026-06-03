import Ember from 'ember';
import config from '../config/environment';

export default Ember.Route.extend({
  // Only inject intl if it exists (optional)
  intl: Ember.inject.service({ optional: true }),

  beforeModel() {
    // Safely set locale only if intl service is available
    let intl = this.get('intl');
    if (intl && typeof intl.setLocale === 'function') {
      intl.setLocale('en-us');
    }
    return this._super(...arguments);
  },

  model() {
    // Fix double /api issue - ApiUrl is already empty string, so just use '/api/stats'
    let url = '/api/stats';
    return Ember.$.getJSON(url).then(function(data) {
      return Ember.Object.create(data);
    }).catch(function(error) {
      console.error('Failed to load stats:', error);
      return Ember.Object.create({});
    });
  },

  setupController(controller, model) {
    this._super(controller, model);
    // Refresh every 10 seconds
    Ember.run.later(this, this.refresh, 10000);
  }
});
